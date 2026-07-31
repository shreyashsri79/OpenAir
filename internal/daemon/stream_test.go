package daemon

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// M11 through the daemon: a remote file on a loopback URL, and a media player's
// Range request coming out the other end as a §11.2 range read.
//
// These use an address rather than a device name for the same reason browse_test
// does -- discovery is off in tests -- and the target is unlocked first, because
// remotefs is Owned.

// streamingPair returns a source sharing dir and a browser unlocked for it.
func streamingPair(t *testing.T, dir string) (source, browser *Daemon, bc *Client) {
	t.Helper()
	source = sharingDaemon(t, "media", dir)
	browser = newProtectedDaemon(t)
	pinOwned(t, browser, source)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	bc = connect(t, browser, nil, nil)
	if _, err := bc.Unlock(ctx, string(source.DeviceID()), []byte(testPassphrase), nil, false, 5*time.Minute); err != nil {
		t.Fatalf("unlock: %v", err)
	}
	return source, browser, bc
}

// mediaFile writes n deterministic bytes into a share.
func mediaFile(t *testing.T, dir, name string, n int) []byte {
	t.Helper()
	content := make([]byte, n)
	rnd := rand.New(rand.NewSource(int64(n) + 7))
	rnd.Read(content)
	if err := os.WriteFile(filepath.Join(dir, name), content, 0o644); err != nil {
		t.Fatal(err)
	}
	return content
}

// get issues one HTTP request against the stream URL, with an optional Range.
func get(t *testing.T, target, rng string) (*http.Response, []byte) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, target, nil)
	if err != nil {
		t.Fatal(err)
	}
	if rng != "" {
		req.Header.Set("Range", rng)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", target, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading the response: %v", err)
	}
	return resp, body
}

// TestStreamingARemoteFileOverLoopback is the milestone as a person meets it:
// ask for a URL, hand it to a player, and have the player's seek land where it
// asked.
func TestStreamingARemoteFileOverLoopback(t *testing.T) {
	dir := t.TempDir()
	want := mediaFile(t, dir, "film.bin", 3<<20)
	source, _, bc := streamingPair(t, dir)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	target, mime, size, err := bc.Stream(ctx, source.Addr(), "media/film.bin")
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	if size != uint64(len(want)) {
		t.Fatalf("size %d, want %d", size, len(want))
	}
	if mime == "" {
		t.Fatal("no MIME type for a streamed file")
	}

	// Loopback only: a URL anything on the network could open would be a file
	// server nobody asked for.
	u, err := url.Parse(target)
	if err != nil {
		t.Fatal(err)
	}
	if host, _, _ := strings.Cut(u.Host, ":"); host != "127.0.0.1" {
		t.Fatalf("the stream URL is on %s, not loopback", u.Host)
	}

	// A player seeking into the middle: one Range request, the bytes that are
	// there, and 206 rather than the whole file.
	const at = 2 << 20
	resp, body := get(t, target, fmt.Sprintf("bytes=%d-%d", at, at+4095))
	if resp.StatusCode != http.StatusPartialContent {
		t.Fatalf("a Range request answered %s", resp.Status)
	}
	if !bytes.Equal(body, want[at:at+4096]) {
		t.Fatal("a Range request returned the wrong bytes")
	}
	if got := resp.Header.Get("Content-Range"); !strings.HasPrefix(got, fmt.Sprintf("bytes %d-%d/", at, at+4095)) {
		t.Fatalf("Content-Range %q", got)
	}

	// And a plain GET is still the whole file, which is what a player does when
	// it decides to buffer from the start.
	resp, body = get(t, target, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("a plain GET answered %s", resp.Status)
	}
	if !bytes.Equal(body, want) {
		t.Fatalf("a plain GET returned %d bytes, want %d, and they differ", len(body), len(want))
	}
}

// TestSeekingBackwardsAndForwardsInOneStream is what scrubbing looks like: a
// player jumps around, and each jump is served from where it asked.
func TestSeekingBackwardsAndForwardsInOneStream(t *testing.T) {
	dir := t.TempDir()
	want := mediaFile(t, dir, "film.bin", 8<<20)
	source, _, bc := streamingPair(t, dir)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	target, _, _, err := bc.Stream(ctx, source.Addr(), "media/film.bin")
	if err != nil {
		t.Fatalf("stream: %v", err)
	}

	for _, at := range []int{7 << 20, 1 << 20, 6 << 20, 0, 8<<20 - 1024} {
		resp, body := get(t, target, fmt.Sprintf("bytes=%d-%d", at, at+1023))
		if resp.StatusCode != http.StatusPartialContent {
			t.Fatalf("range at %d answered %s", at, resp.Status)
		}
		if !bytes.Equal(body, want[at:at+1024]) {
			t.Fatalf("range at %d returned the wrong bytes", at)
		}
	}
}

// TestAStreamURLIsUnguessableAndWithdrawable. The token is the only thing
// between another process on this machine and someone else's files, and a
// stream that cannot be stopped is a share nobody agreed to.
func TestAStreamURLIsUnguessableAndWithdrawable(t *testing.T) {
	dir := t.TempDir()
	mediaFile(t, dir, "film.bin", 1<<20)
	source, _, bc := streamingPair(t, dir)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	target, _, _, err := bc.Stream(ctx, source.Addr(), "media/film.bin")
	if err != nil {
		t.Fatalf("stream: %v", err)
	}

	// The path is a token, not the file's name: a URL that named the remote
	// path would be guessable by anyone who knew what was shared.
	u, _ := url.Parse(target)
	token := strings.Split(strings.TrimPrefix(u.Path, "/s/"), "/")[0]
	if len(token) < 24 {
		t.Fatalf("a %d-character stream token", len(token))
	}

	wrong := strings.Replace(target, token, strings.Repeat("a", len(token)), 1)
	if resp, _ := get(t, wrong, ""); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("a wrong token answered %s", resp.Status)
	}

	// Asking twice for the same file reuses the URL rather than publishing a
	// second one.
	again, _, _, err := bc.Stream(ctx, source.Addr(), "media/film.bin")
	if err != nil {
		t.Fatalf("second stream: %v", err)
	}
	if again != target {
		t.Fatalf("asking twice published two URLs:\n%s\n%s", target, again)
	}

	if err := bc.StopStream(ctx, source.Addr(), "media/film.bin"); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if resp, _ := get(t, target, ""); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("a withdrawn URL answered %s", resp.Status)
	}
}

// TestStreamingIsRefusedWithoutAnUnlock. remotefs is Owned, and streaming is
// remotefs: a locked browser gets the same refusal browsing gets, and it says
// what to do about it.
func TestStreamingIsRefusedWithoutAnUnlock(t *testing.T) {
	dir := t.TempDir()
	mediaFile(t, dir, "film.bin", 1<<20)

	source := sharingDaemon(t, "media", dir)
	browser := newProtectedDaemon(t)
	pinOwned(t, browser, source)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	bc := connect(t, browser, nil, nil)
	_, _, _, err := bc.Stream(ctx, source.Addr(), "media/film.bin")
	if err == nil {
		t.Fatal("a locked device was given a stream URL")
	}
	if !strings.Contains(err.Error(), "unlock") {
		t.Fatalf("the refusal does not mention unlocking: %v", err)
	}
}

// TestStreamingADirectoryIsRefused, because the answer would otherwise be an
// empty file a player would sit on.
func TestStreamingADirectoryIsRefused(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "season1"), 0o755); err != nil {
		t.Fatal(err)
	}
	source, _, bc := streamingPair(t, dir)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	if _, _, _, err := bc.Stream(ctx, source.Addr(), "media/season1"); err == nil {
		t.Fatal("a directory was published as a stream")
	}
}

// TestTheStreamServerStopsWithTheDaemon. It is a listening socket holding
// another device's files open; it must not outlive the thing that made it.
func TestTheStreamServerStopsWithTheDaemon(t *testing.T) {
	dir := t.TempDir()
	mediaFile(t, dir, "film.bin", 1<<20)
	source, browser, bc := streamingPair(t, dir)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	target, _, _, err := bc.Stream(ctx, source.Addr(), "media/film.bin")
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	if resp, _ := get(t, target, ""); resp.StatusCode != http.StatusOK {
		t.Fatalf("the stream answered %s before shutdown", resp.Status)
	}

	browser.Close()

	waitFor(t, "the stream server to stop listening", func() bool {
		req, _ := http.NewRequest(http.MethodGet, target, nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return true
		}
		resp.Body.Close()
		return false
	})
}
