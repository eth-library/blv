package helpers

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
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

// EnsureDir legt path an, falls es noch nicht existiert. Läuft ohne
// Rückfrage (im Gegensatz zum früheren, interaktiven ToBeCreated), da der
// Aufruf auch beim unbeaufsichtigten Start als systemd-Service passieren muss.
func EnsureDir(path string) error {
	if CheckIfDir(path) {
		return nil
	}
	if err := os.MkdirAll(path, 0o750); err != nil && !os.IsExist(err) {
		return err
	}
	fmt.Println("Verzeichnis angelegt: " + path)
	return nil
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

// TarGzDir packt alle Dateien aus sourceDir (nicht rekursiv - passend für die
// flachen whitelistPath/blocklistPath-Verzeichnisse) in ein gzip-komprimiertes
// tar-Archiv und schreibt es nach destFile.
func TarGzDir(sourceDir, destFile string) error {
	entries, err := os.ReadDir(sourceDir)
	if err != nil {
		return err
	}

	out, err := os.Create(destFile)
	if err != nil {
		return err
	}
	defer out.Close()

	gzw := gzip.NewWriter(out)
	tw := tar.NewWriter(gzw)

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		data, err := os.ReadFile(filepath.Join(sourceDir, entry.Name()))
		if err != nil {
			return err
		}
		header := &tar.Header{
			Name:    entry.Name(),
			Mode:    int64(info.Mode().Perm()),
			Size:    int64(len(data)),
			ModTime: info.ModTime(),
		}
		if err := tw.WriteHeader(header); err != nil {
			return err
		}
		if _, err := tw.Write(data); err != nil {
			return err
		}
	}

	if err := tw.Close(); err != nil {
		return err
	}
	return gzw.Close()
}

// ParseEnvFile liest eine einfache .env-Datei (KEY=VALUE pro Zeile,
// '#'-Kommentare und Leerzeilen werden ignoriert).
func ParseEnvFile(path string) (map[string]string, error) {
	values := make(map[string]string)

	file, err := os.Open(path)
	if err != nil {
		return values, err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, found := strings.Cut(line, "=")
		if !found {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		value = strings.Trim(value, `"'`)
		if key != "" {
			values[key] = value
		}
	}
	if err := scanner.Err(); err != nil {
		return values, err
	}
	return values, nil
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
