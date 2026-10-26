// lab is the playground's toolbox image: small commands that show what a
// container sees and does, one subcommand each.
//
//	lab                 lists the subcommands
//	lab exit N          writes "bye" to stderr and exits N
//	lab echo ARGS...    prints its arguments
//	lab esc TEXT        prints TEXT with \e as ESC and \a as BEL
//	lab env             prints the environment
//	lab pwd             prints the working directory
//	lab cat PATH        prints a file
//	lab ls [PATH]       lists a directory
//	lab write PATH TXT  writes a file (inside this container only)
//	lab count N [MS]    prints 1..N, one line every MS milliseconds (default 500)
//	lab sleep MS        sleeps
//	lab spin            loops for ever (see the run timeout end it)
//	lab hog             allocates until memory runs out (see the memory limit)
//	lab bench N         times N stat calls (the WASI round trip)
//	lab fs              a file-system workout: mkdir, write, rename, symlink, remove
package main

import (
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

func usage() {
	fmt.Println("usage: lab <exit|echo|env|pwd|cat|ls|write|count|sleep|spin|hog|bench|fs> ...")
}

func main() {
	if len(os.Args) < 2 {
		usage()
		return
	}
	args := os.Args[2:]
	arg := func(i int, def string) string {
		if i < len(args) {
			return args[i]
		}
		return def
	}
	num := func(i int, def int) int {
		n, err := strconv.Atoi(arg(i, strconv.Itoa(def)))
		if err != nil {
			fmt.Fprintln(os.Stderr, "lab: not a number:", arg(i, ""))
			os.Exit(2)
		}
		return n
	}
	switch os.Args[1] {
	case "exit":
		fmt.Fprintln(os.Stderr, "bye")
		os.Exit(num(0, 3))
	case "echo":
		fmt.Println(strings.Join(args, " "))
	case "esc":
		// Prints its argument with \e as ESC and \a as BEL: shows what the
		// playground's terminal filter lets through.
		s := strings.NewReplacer(`\e`, "\x1b", `\a`, "\a").Replace(strings.Join(args, " "))
		fmt.Println(s)
	case "env":
		env := os.Environ()
		sort.Strings(env)
		for _, e := range env {
			fmt.Println(e)
		}
	case "pwd":
		wd, err := os.Getwd()
		check(err)
		fmt.Println(wd)
	case "cat":
		b, err := os.ReadFile(arg(0, ""))
		check(err)
		os.Stdout.Write(b)
	case "ls":
		ents, err := os.ReadDir(arg(0, "."))
		check(err)
		for _, e := range ents {
			name := e.Name()
			if e.IsDir() {
				name += "/"
			}
			fmt.Println(name)
		}
	case "write":
		check(os.WriteFile(arg(0, ""), []byte(arg(1, "")+"\n"), 0o644))
	case "count":
		n, ms := num(0, 5), num(1, 500)
		for i := 1; i <= n; i++ {
			fmt.Println(i)
			if i < n {
				time.Sleep(time.Duration(ms) * time.Millisecond)
			}
		}
	case "sleep":
		time.Sleep(time.Duration(num(0, 1000)) * time.Millisecond)
	case "spin":
		for i := 0; ; i++ {
			if i < 0 {
				fmt.Println(i)
			}
		}
	case "hog":
		var keep [][]byte
		for {
			b := make([]byte, 1<<20)
			for i := range b {
				b[i] = byte(i)
			}
			keep = append(keep, b)
		}
	case "bench":
		n := num(0, 1000)
		t0 := time.Now()
		for i := 0; i < n; i++ {
			if _, err := os.Stat("/"); err != nil {
				check(err)
			}
		}
		d := time.Since(t0)
		fmt.Printf("%d stat calls in %v: %.1f µs each\n", n, d.Round(time.Microsecond), float64(d.Microseconds())/float64(n))
	case "fs":
		check(os.MkdirAll("/tmp/lab/sub", 0o755))
		check(os.WriteFile("/tmp/lab/a.txt", []byte("alpha"), 0o644))
		check(os.Rename("/tmp/lab/a.txt", "/tmp/lab/sub/b.txt"))
		check(os.Symlink("sub/b.txt", "/tmp/lab/link"))
		b, err := os.ReadFile("/tmp/lab/link")
		check(err)
		fmt.Println("via link:", string(b))
		check(os.Remove("/tmp/lab/link"))
		check(os.RemoveAll("/tmp/lab"))
		fmt.Println("ok")
	default:
		usage()
		os.Exit(2)
	}
}

func check(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "lab:", err)
		os.Exit(1)
	}
}
