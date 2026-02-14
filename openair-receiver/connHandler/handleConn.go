package connhandler

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	errorhandler "github.com/shreyashsri79/openair-receiver/errorHandler"
	"github.com/shreyashsri79/openair-receiver/file"
	"github.com/shreyashsri79/openair-receiver/models"
)

const ReadTimeoutSec = 30

func HandleConn(conn net.Conn) {
	defer conn.Close()

	addr := conn.RemoteAddr().String()
	fmt.Println("Incoming connection from address :" + addr)

	conn.SetReadDeadline(time.Now().Add(ReadTimeoutSec * time.Second))

	reader := bufio.NewReader(conn)

	headerLine, err := reader.ReadString('\n')
	if err != nil {
		errorhandler.FatalRed("Cant read header ", err)
		return
	}

	headerLine = strings.TrimSpace(headerLine)

	var meta models.FileMetaData

	if err := json.Unmarshal([]byte(headerLine), &meta); err != nil {
		errorhandler.FatalRed("Error unmarshing the meta data", err)
		return
	}

	fmt.Println("file name :" + meta.Name)
	fmt.Println("file size :" + strconv.FormatInt(meta.Size,10))
	fmt.Println("file sha :" + meta.SHA256)

	file.ValidateAndSanitizeFile(&meta)

	fmt.Printf("Incoming file request:\n")
	fmt.Printf("  From: %s\n", addr)
	fmt.Printf("  File: %s\n", meta.Name)
	fmt.Printf("  Size: %s\n", file.FormatBytes(meta.Size))
	fmt.Printf("Accept? (\033[32my\033[0m/\033[31mn\033[0m): ")

	in := bufio.NewReader(os.Stdin)
	answer, _ := in.ReadString('\n')
	answer = strings.TrimSpace(answer)

	if answer != "y" && answer != "yes" {
		fmt.Printf("\033[1;31mConnection Rejected\033[0m")
		return
	}

	conn.Write([]byte("ACCEPT\n"))
	fmt.Println("\033[32mAccepted. Receiving...\033[0m")

	conn.SetReadDeadline(time.Time{})

	saveDir, err := file.GetOpenAirDir()
	if err != nil {
		errorhandler.FatalRed("Trouble getting the save to directory", err)
		return
	}

	finalPath := file.UniquePath(filepath.Join(saveDir, meta.Name))
	tempPath := finalPath + ".part"

	out, err := os.Create(tempPath)
	if err != nil {
		errorhandler.FatalRed("Couldnot create file path", err)
		return
	}
	defer out.Close()

	errMsg, err := file.RecieveFile(reader, out, &meta)
	if err != nil {
		errorhandler.FatalRed(errMsg, err)
		os.Remove(tempPath)
		return
	}

	if err := os.Rename(tempPath, finalPath); err != nil {
		errorhandler.FatalRed("failed to finalise file", err)
		os.Remove(tempPath)
		return
	}

	fmt.Println("\033[32mTransfer complete!\033[0m")
	fmt.Println("\033[32mSaved to:\033[0m", finalPath)

}
