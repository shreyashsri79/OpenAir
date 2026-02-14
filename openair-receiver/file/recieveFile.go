package file

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"strings"

	"github.com/shreyashsri79/openair-receiver/models"
)

func RecieveFile(reader *bufio.Reader, out *os.File, meta *models.FileMetaData) (string,error) {
	hasher := sha256.New()

	limited := io.LimitReader(reader, meta.Size)
	written, err := io.Copy(io.MultiWriter(out,hasher), limited)
	if err != nil {return  "Trannsfer Failed",err}

	if written != meta.Size {return "Transfer Incomplete",err}

	gotHash :=  hex.EncodeToString(hasher.Sum(nil))
	if !strings.EqualFold(gotHash, meta.SHA256) {
		return "",errors.New("SHA-256 mismatch")
	}

	return "",nil
}
