package main

import (
	"flag"
	"go/build"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-fsnotify/fsnotify"
)

var (
	verbose = flag.Bool("v", false, "verbose output")
)

func main() {
	log.SetFlags(0)
	flag.Parse()

	if flag.NArg() == 0 {
		log.Fatal("must provide package path")
	}

	pkgPath := flag.Arg(0)
	cmdArgs := flag.Args()[1:]

	wd, err := os.Getwd()
	if err != nil {
		log.Fatal(err)
	}

	pkg, err := build.Import(pkgPath, wd, 0)
	if err != nil {
		log.Fatal(err)
	}

	if *verbose {
		log.Printf("Watching package %s", pkg.ImportPath)
	}

	w, err := fsnotify.NewWatcher()
	if err != nil {
		log.Fatal(err)
	}

	pkgs := []*build.Package{pkg}
	seenPkgs := map[string]struct{}{}
	for i := 0; i < len(pkgs); i++ {
		pkg := pkgs[i]
		if err := w.Add(pkg.Dir); err != nil {
			log.Fatal(err)
		}
		for _, file := range pkg.GoFiles {
			srcFile := filepath.Join(pkg.Dir, file)
			if *verbose {
				log.Printf("Watch %s", srcFile)
			}
			if err := w.Add(srcFile); err != nil {
				log.Fatal(err)
			}
		}
		for _, imp := range pkg.Imports {
			if _, seen := seenPkgs[imp]; !seen {
				if imp == "C" {
					continue
				}
				impPkg, err := build.Import(imp, "", 0)
				if err != nil {
					log.Fatal(err)
				}
				pkgs = append(pkgs, impPkg)
				seenPkgs[imp] = struct{}{}
			}
		}
	}

	restart := make(chan struct{})
	go func() {
		var proc *os.Process
		for v := range restart {
			_ = v
			if proc != nil {
				if err := proc.Signal(os.Interrupt); err != nil {
					log.Println(err)
					proc.Kill()
				}
				proc.Wait()
			}
			cmd := exec.Command(filepath.Join(pkg.BinDir, filepath.Base(pkg.ImportPath)), cmdArgs...)
			cmd.Stdout = os.Stdout
			cmd.Stderr = os.Stderr
			if *verbose {
				log.Println(cmd.Args)
			}
			if err := cmd.Start(); err != nil {
				log.Println(err)
			}
			proc = cmd.Process
		}
	}()
	restart <- struct{}{}

	install := make(chan struct{})
	go func() {
		needsInstall := 0
		for {
			var timerChan <-chan time.Time
			if needsInstall > 0 {
				timerChan = time.After(200 * time.Millisecond)
			} else {
				timerChan = make(chan time.Time) // never sent on, blocks indefinitely
			}
			select {
			case <-install:
				needsInstall++
				continue
			case <-timerChan:
				needsInstall = 0
				cmd := exec.Command("go", "install", pkg.ImportPath)
				cmd.Stdout = os.Stdout
				cmd.Stderr = os.Stderr
				if *verbose {
					log.Printf("go install %s", pkg.ImportPath)
				}
				if err := cmd.Run(); err != nil {
					log.Println(err)
				}
				restart <- struct{}{}
			}
		}
	}()

	for {
		select {
		case ev, ok := <-w.Events:
			if !ok {
				break
			}

			if ext := filepath.Ext(ev.Name); ext != ".go" {
				continue
			}
			if strings.HasPrefix(filepath.Base(ev.Name), ".") {
				continue
			}

			switch ev.Op {
			case fsnotify.Create:
				if err := w.Add(ev.Name); err != nil {
					log.Println(err)
				}
			case fsnotify.Remove:
				if err := w.Remove(ev.Name); err != nil {
					log.Println(err)
				}
			case fsnotify.Chmod:
				continue
			}
			if *verbose {
				log.Println(ev)
			}
			install <- struct{}{}
		case err, ok := <-w.Errors:
			if !ok {
				break
			}
			if ok {
				log.Fatal(err)
			}
		}
	}
}