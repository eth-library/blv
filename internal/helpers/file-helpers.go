package helpers

import (
	"bufio"
	"fmt"
	"hash"
	"io"
	"os"
	"path/filepath"
	"strings"
)

func GetCleanPath(path string) string {
	return filepath.Clean(path)
}

func Checknaddtrailingslash(path *string) {
	if !strings.HasSuffix(*path, "/") {
		*path = *path + "/"
	}
}

func CheckIfDir(path string) bool {
	fileInfo, err := os.Stat(path)
	if err != nil {
		fmt.Println("DEBUG", err)
		return false
	} else {
		if fileInfo.IsDir() {
			return true
		} else {
			// TODO: catch error if it is a file and not a directory
			return false
		}
	}
}

func ToBeCreated(path string) {
	fmt.Println("the folder " + path + " is missing")
	var anlegen string
	yes := []string{"j", "J", "y", "Y"}
	fmt.Print("shall I create it? (y|n) [n]: ")
	fmt.Scanln(&anlegen)
	if StringInSlice(anlegen, yes) {
		if err := os.MkdirAll(path, 0o750); err != nil && !os.IsExist(err) {
			fmt.Println(err)
		} else {
			fmt.Println("OK, " + path + " successfully created")
		}
	} else {
		fmt.Println("the folder " + path + " does not exist but is required")
		fmt.Println("the service will shut down now")
	}
}

func FileExists(filename string) bool {
	info, err := os.Stat(filename)
	if os.IsNotExist(err) {
		return false
	}
	return !info.IsDir()
}

func SeparateFileFromPath(fullpath string) (path string, filename string) {
	filename = filepath.Base(fullpath)
	path = filepath.Dir(fullpath)
	return path, filename
}

func BackupFiles(sourcePath, fileExtension, backupPath string) error {
	filesBackuped := 0
	if err := os.MkdirAll(backupPath, 0o750); err != nil && !os.IsExist(err) {
		fmt.Println(err)
		return err
	}

	entries, err := os.ReadDir(sourcePath)
	if err != nil {
		fmt.Printf("Fehler beim Lesen der Dateien zum Backup: %v", err)
		return err
	}

	for _, file2backup := range entries {
		if filepath.Ext(file2backup.Name()) == fileExtension {
			// file kopieren
			srcPath := filepath.Join(sourcePath, file2backup.Name())
			dstPath := filepath.Join(backupPath, file2backup.Name())

			data, err := os.ReadFile(srcPath)
			if err != nil {
				return err
			}
			// Rechte ggf. aus Stat übernehmen
			if err := os.WriteFile(dstPath, data, 0o644); err != nil {
				return err
			}
			filesBackuped++
		}
	}
	fmt.Println(filesBackuped, "Dateien von", sourcePath, "nach", backupPath, "gesichert")
	return nil
}

func CheckSum(hashAlgorithm hash.Hash, filename string) (string, error) {
	file, err := os.Open(filename)
	if err != nil {
		return "", err
	}
	defer func() { _ = file.Close() }()

	buf := make([]byte, 65536)
	for {
		switch n, err := bufio.NewReader(file).Read(buf); err {
		case nil:
			hashAlgorithm.Write(buf[:n])
		case io.EOF:
			return fmt.Sprintf("%x", hashAlgorithm.Sum(nil)), nil
		default:
			return "", err
		}
	}
}
