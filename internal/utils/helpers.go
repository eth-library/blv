package utils

import (
	"bufio"
	"flag"
	"fmt"
	"hash"
	"io"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// ===============
// general helpers
// ===============

func FlagIsPassed(name string) bool {
	found := false
	flag.Visit(func(f *flag.Flag) {
		if f.Name == name {
			found = true
		}
	})
	return found
}

func StringInSlice(name string, sl []string) bool {
	return slices.Contains(sl, name)
}

func GetStringSliceElementIndex(slice []string, value string) int {
	for i, v := range slice {
		if v == value {
			return i
		}
	}
	return -1
}

func GetCleanPath(path string) string {
	return filepath.Clean(path)
}

func checknaddtrailingslash(path *string) {
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

func Sort_map_by_value_desc() {}
func Sort_map_by_value_asc()  {}

func BuildOutputHeader(logfile string, datetime string, timestamps []string, infos map[string]string) string {
	header := "We analyzed "
	if len(timestamps) == 2 {
		header += "the time between " + timestamps[0] + " and " + timestamps[1] + " in\n"
	}
	header += "the file: " + logfile
	header += "\n================================================================================\n"
	for key, count := range infos {
		header += "\n\t" + key + "\t: " + count
	}
	header += "\n"
	return header
}

func IPToUint32(ip net.IP) uint32 {
	ip = ip.To4()
	if ip == nil {
		return 0
	}
	return uint32(ip[0])<<24 | uint32(ip[1])<<16 | uint32(ip[2])<<8 | uint32(ip[3])
}

func GetIPRange(cidr string) (uint32, uint32, error) {
	_, ipNet, err := net.ParseCIDR(cidr)
	if err != nil {
		return 0, 0, err
	}
	start := IPToUint32(ipNet.IP)
	mask := IPToUint32(net.IP(ipNet.Mask))
	end := start | (^mask)
	return start, end, nil
}
