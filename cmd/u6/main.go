// Command u6 runs Ultima VI: The False Prophet (the DOS VGA version) from the original data
// files: ULTIMA6.EXE's job, the intro and character creation (U.EXE), the game (GAME.EXE)
// and the ending (END.EXE), each ported to Go.
//
//	u6 [-data DIR]            play with the game files in DIR (default $GOPLAY_DATA, else ./game)
//	u6 -import SRC -data DIR  copy the game files out of SRC (folder, zip, ISO/BIN/CUE, GOG
//	                          installer, loose files) into DIR, write DIR/COVER.PNG and exit
//	u6 -import-cd SRC -data DIR  keep the FM Towns CD's voices, English conversations and
//	                          sampled sound effects from SRC under DIR/CD and exit
package main

import (
	"flag"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/memmaker/u6-go/internal/host"
	"github.com/retro-ware/gameimport"
)

// markerFiles prove a folder holds Ultima VI.
var markerFiles = []string{"MAPTILES.VGA", "LZMAP", "CONVERSE.A"}

// Permissions of the directories and files written.
const (
	dirMode  = 0o755
	fileMode = 0o644
)

// gameFile says whether name is one of the game's data files: everything on the original
// disks but the programs and drivers for hardware the port does not emulate.
func gameFile(name string) bool {
	base := strings.ToUpper(filepath.Base(name))
	switch strings.ToUpper(filepath.Ext(base)) {
	case ".EXE", ".BAT", ".BIN", ".IMG", ".IMA", ".ISO", ".ZIP", ".CUE", ".GOG", ".7Z", ".COM", ".DLL", ".INI", ".TXT", ".PDF", ".DOC", ".CGA", ".EGA", ".TGA":
		return false
	}
	return !strings.ContainsAny(base, " ")
}

func main() {
	defaultDir := os.Getenv("GOPLAY_DATA")
	if defaultDir == "" {
		defaultDir = "game"
		if _, err := os.Stat("data"); err == nil {
			defaultDir = "data" // goplay: started in games/u6 with no arguments
		}
	}
	dir := flag.String("data", defaultDir, "directory with the original game files")
	source := flag.String("import", "", "import the game files from this folder/zip/ISO/installer into -data and exit")
	cdSource := flag.String("import-cd", "", "import the FM Towns CD's voices and sound effects from this disc image/folder into -data/CD and exit")
	scale := flag.Int("scale", 3, "window scale")
	roms := flag.String("roms", os.Getenv("GOPLAY_MT32"), "the MT-32 ROM directory for music (default: roms/ in the working directory or next to the binary; none: AdLib)")
	flag.Parse()

	if *source != "" {
		if err := importGame(*source, *dir); err != nil {
			fmt.Fprintln(os.Stderr, "import:", err)
			os.Exit(1)
		}
		return
	}
	if *cdSource != "" {
		if err := importCD(*cdSource, *dir); err != nil {
			fmt.Fprintln(os.Stderr, "import-cd:", err)
			os.Exit(1)
		}
		return
	}
	if _, err := os.Stat(filepath.Join(*dir, markerFiles[0])); err != nil {
		fmt.Fprintf(os.Stderr, "no game files in %s; use -import SRC or -data DIR\n", *dir)
		os.Exit(1)
	}
	var gameErr error
	err := host.Run("Ultima VI", *scale, func() { gameErr = run(*dir, romDir(*roms)) })
	if err == nil {
		err = gameErr
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// importGame copies the game files found in source into dir and writes the cover picture.
func importGame(source, dir string) error {
	files, err := gameimport.Find(source, gameFile)
	if err != nil {
		return err
	}
	have := map[string]bool{}
	for name := range files {
		have[strings.ToUpper(filepath.Base(name))] = true
	}
	for _, marker := range markerFiles {
		if !have[marker] {
			return fmt.Errorf("no Ultima VI files (%s) found in %s", marker, source)
		}
	}
	if err := os.MkdirAll(dir, dirMode); err != nil {
		return err
	}
	for name, contents := range files {
		if err := os.WriteFile(filepath.Join(dir, strings.ToUpper(filepath.Base(name))), contents, fileMode); err != nil {
			return err
		}
	}
	return writeCover(dir)
}

// cdFiles are the FM Towns CD's files the port uses besides SPEECH/*.SAM: the English
// conversations with the voice marks (CONVERSE.C and D are the Japanese ones) and the
// sampled sound effects.
var cdFiles = []string{"CONVERSE.A", "CONVERSE.B", "SOUNDS1.DAT", "SOUNDS2.DAT"}

// importCD copies the FM Towns CD's English voices, conversations and sound effects found in
// source into dir/CD.
func importCD(source, dir string) error {
	files, err := gameimport.Find(source, func(name string) bool {
		base := strings.ToUpper(path.Base(name))
		if strings.EqualFold(path.Base(path.Dir(name)), "SPEECH") {
			return strings.HasSuffix(base, ".SAM")
		}
		return slices.Contains(cdFiles, base)
	})
	if err != nil {
		return err
	}
	out := map[string][]byte{}
	for name, contents := range files {
		base := strings.ToUpper(path.Base(name))
		if strings.HasSuffix(base, ".SAM") {
			base = "SPEECH/" + base
		}
		out[base] = contents
	}
	for _, name := range cdFiles {
		if out[name] == nil {
			return fmt.Errorf("no FM Towns Ultima VI files (%s) found in %s", name, source)
		}
	}
	if err := os.MkdirAll(filepath.Join(dir, "CD", "SPEECH"), dirMode); err != nil {
		return err
	}
	for name, contents := range out {
		if err := os.WriteFile(filepath.Join(dir, "CD", filepath.FromSlash(name)), contents, fileMode); err != nil {
			return err
		}
	}
	return nil
}

// romDir is -roms, else the first roms/ directory found in the working directory or next
// to the binary ("" if none).
func romDir(flagDir string) string {
	if flagDir != "" {
		return flagDir
	}
	dirs := []string{"roms"}
	if exe, err := os.Executable(); err == nil {
		dirs = append(dirs, filepath.Join(filepath.Dir(exe), "roms"))
	}
	for _, d := range dirs {
		if fi, err := os.Stat(d); err == nil && fi.IsDir() {
			return d
		}
	}
	return ""
}
