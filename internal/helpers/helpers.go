package helpers

import (
	"flag"
	"slices"
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
