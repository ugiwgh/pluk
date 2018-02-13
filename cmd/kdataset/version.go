package main

import (
	"fmt"
	"runtime"
)

type Version struct {
	version    string
	goCompiler string
}

func (v Version) String() string {
	return fmt.Sprintf("%v", v.version)
}

func GetVersion() Version {
	return Version{
		version:    "1.0.10",
		goCompiler: runtime.Version(),
	}
}
