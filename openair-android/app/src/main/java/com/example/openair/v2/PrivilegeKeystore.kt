package com.example.openair.v2

import android.content.Context
import android.os.Build
import android.security.keystore.KeyGenParameterSpec
import android.security.keystore.KeyPermanentlyInvalidatedException
import android.security.keystore.KeyProperties
import android.security.keystore.UserNotAuthenticatedException
import java.io.File
import java.security.KeyStore
import java.security.SecureRandom
import javax.crypto.Cipher
import javax.crypto.KeyGenerator
import javax.crypto.SecretKey
import javax.crypto.spec.GCMParameterSpec

/**
 * PrivilegeKeystore is Android's tier 1 (D-21): the key-encryption key that
 * opens this device's privilege key is released only after the user has
 * authenticated to the system.
 *
 * The shape needs explaining, because a simpler-looking design does not work.
 * The Go core needs 32 raw bytes to open the Appendix A container, and Android
 * Keystore will never export key material -- that is the property that makes it
 * worth using. So the Keystore key is not the KEK; it *wraps* one:
 *
 *   1. A 32-byte KEK is generated once, at Protect time, and handed to Go.
 *   2. The same bytes are sealed with an AES-GCM key held inside the Keystore,
 *      and only the sealed blob is written to app-private storage.
 *   3. Unlocking decrypts that blob, which the Keystore permits only while the
 *      user has authenticated within the validity window.
 *
 * The window is D-18's six hours, enforced by the platform rather than by a
 * timer in this process -- which is exactly the refinement D-19 asks for. Go
 * keeps its own six-hour timer over the decrypted privilege key; the two agree
 * by construction because both start at the same authentication.
 *
 * What this does not defend against: a device whose screen lock is not set. The
 * Keystore requirement cannot be created at all in that case, and
 * [isDeviceSecure] is what the UI must check before offering any of this.
 */
object PrivilegeKeystore {

    private const val KEY_ALIAS = "openair.v2.privilege.kek"
    private const val WRAPPED_FILE = "privilege.kek"
    private const val TRANSFORMATION = "AES/GCM/NoPadding"
    private const val GCM_TAG_BITS = 128
    private const val KEK_BYTES = 32

    /** Six hours, D-18's default, expressed where the platform enforces it. */
    const val AUTH_VALIDITY_SECONDS = 6 * 60 * 60

    class NeedsAuthentication : Exception("authenticate to unlock owned access")
    class NeedsReprotect(cause: Throwable) :
        Exception("the screen lock changed, so this device's privilege key can no longer be opened", cause)

    /** Whether the device has a screen lock at all; without one there is no tier 1. */
    fun isDeviceSecure(context: Context): Boolean {
        val km = context.getSystemService(android.app.KeyguardManager::class.java)
        return km?.isDeviceSecure == true
    }

    fun hasWrappedKek(context: Context): Boolean = wrappedFile(context).exists()

    /**
     * Creates the key-encryption key and returns it once, for [mobile.Identity.protect].
     *
     * Called with the user having just authenticated. The returned bytes must not
     * be stored by the caller: the copy that persists is the sealed one written
     * here, and everything else is the Go core's business.
     */
    fun createKek(context: Context): ByteArray {
        require(isDeviceSecure(context)) { "this device has no screen lock, so it cannot protect a privilege key" }

        val kek = ByteArray(KEK_BYTES).also { SecureRandom().nextBytes(it) }
        // Sealing needs the user too: a key created with user-authentication
        // required cannot even encrypt until someone has authenticated, which is
        // the same gate the unlock path meets and is handled the same way.
        val cipher = Cipher.getInstance(TRANSFORMATION)
        try {
            cipher.init(Cipher.ENCRYPT_MODE, generateKeystoreKey())
        } catch (e: UserNotAuthenticatedException) {
            throw NeedsAuthentication()
        }
        val sealed = cipher.doFinal(kek)

        // iv || ciphertext. The IV is not secret and the AEAD tag is inside the
        // ciphertext, so the file needs no framing beyond the fixed-length IV.
        wrappedFile(context).writeBytes(cipher.iv + sealed)
        return kek
    }

    /**
     * Returns the key-encryption key, or throws [NeedsAuthentication] when the
     * user must authenticate first.
     *
     * The exception is the whole point: this is the moment the platform enforces
     * user presence, and the caller's job is to run the credential prompt and
     * try again rather than to work around it.
     */
    fun unlockKek(context: Context): ByteArray {
        val blob = wrappedFile(context).takeIf { it.exists() }?.readBytes()
            ?: throw IllegalStateException("this device has no privilege key yet")
        val ivLength = 12
        require(blob.size > ivLength) { "the sealed key-encryption key is truncated" }

        val key = existingKeystoreKey() ?: throw IllegalStateException("the keystore entry is gone")
        return try {
            val cipher = Cipher.getInstance(TRANSFORMATION).apply {
                init(Cipher.DECRYPT_MODE, key, GCMParameterSpec(GCM_TAG_BITS, blob, 0, ivLength))
            }
            cipher.doFinal(blob, ivLength, blob.size - ivLength)
        } catch (e: UserNotAuthenticatedException) {
            throw NeedsAuthentication()
        } catch (e: KeyPermanentlyInvalidatedException) {
            // Changing or removing the screen lock destroys the Keystore key,
            // and with it the only way to open the privilege key. Owned access
            // has to be set up again; pairings and transfers are unaffected,
            // because those ride the identity key (D-20).
            throw NeedsReprotect(e)
        }
    }

    /** Forgets the sealed key. Used when re-protecting after a lock change. */
    fun clear(context: Context) {
        wrappedFile(context).delete()
        runCatching { keyStore().deleteEntry(KEY_ALIAS) }
    }

    private fun wrappedFile(context: Context): File =
        File(File(context.filesDir, "openair").apply { mkdirs() }, WRAPPED_FILE)

    private fun keyStore(): KeyStore = KeyStore.getInstance("AndroidKeyStore").apply { load(null) }

    private fun existingKeystoreKey(): SecretKey? =
        keyStore().getKey(KEY_ALIAS, null) as? SecretKey

    private fun generateKeystoreKey(): SecretKey {
        val generator = KeyGenerator.getInstance(KeyProperties.KEY_ALGORITHM_AES, "AndroidKeyStore")
        val spec = KeyGenParameterSpec.Builder(
            KEY_ALIAS,
            KeyProperties.PURPOSE_ENCRYPT or KeyProperties.PURPOSE_DECRYPT,
        )
            .setBlockModes(KeyProperties.BLOCK_MODE_GCM)
            .setEncryptionPaddings(KeyProperties.ENCRYPTION_PADDING_NONE)
            .setUserAuthenticationRequired(true)
            .apply {
                if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.R) {
                    setUserAuthenticationParameters(
                        AUTH_VALIDITY_SECONDS,
                        KeyProperties.AUTH_DEVICE_CREDENTIAL or KeyProperties.AUTH_BIOMETRIC_STRONG,
                    )
                } else {
                    @Suppress("DEPRECATION")
                    setUserAuthenticationValidityDurationSeconds(AUTH_VALIDITY_SECONDS)
                }
            }
            .build()
        generator.init(spec)
        return generator.generateKey()
    }
}
