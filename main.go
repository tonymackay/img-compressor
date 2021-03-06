package main

import (
	"bufio"
	"bytes"
	"crypto/md5"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"

	"github.com/gobwas/glob"
)

var (
	filename    = "img-compressor"
	showVersion bool
	showHelp    bool
	version     = "dev"
	dryRun      bool
	inputDir    string
	exclude     string
	verbose     bool
	jpegQuality int64
	workers     int
	outputPath  = filename + ".txt"
	compressed  = make(map[string]struct{})
)

func init() {
	flag.BoolVar(&showVersion, "version", false, "Print version number")
	flag.BoolVar(&showHelp, "help", false, "Show help")
	flag.BoolVar(&dryRun, "dryrun", false, "Run command without making changes")
	flag.BoolVar(&verbose, "verbose", false, "Print a verbose output")
	flag.StringVar(&inputDir, "input-dir", "", "Path to a directory containing images to compress")
	flag.StringVar(&exclude, "exclude", "", "Glob pattern of directories/images to exclude, e.g {\".git,*.jpg\"}")
	flag.Int64Var(&jpegQuality, "jpeg-quality", 84, "Visual quality to aim for expressed as a JPEG quality value")
	flag.IntVar(&workers, "workers", runtime.NumCPU(), "The number of concurrent proccesses to run")
	flag.Usage = usage
}

func main() {
	flag.Parse()

	if showVersion {
		fmt.Printf("%s %s (runtime: %s)\n", filename, version, runtime.Version())
		os.Exit(0)
	}

	if showHelp {
		flag.Usage()
		os.Exit(0)
	}

	if inputDir == "" {
		fmt.Println("error: -input-dir is required")
		flag.Usage()
		os.Exit(2)
	}

	info, err := os.Stat(inputDir)
	if os.IsNotExist(err) {
		fmt.Println("error: path does not exist")
		os.Exit(2)
	}

	if !info.IsDir() {
		fmt.Println("error: specified path is not a directory")
		os.Exit(2)
	}

	if jpegQuality < 84 {
		fmt.Println("error: jpeg-quality must be 84 or greater")
		os.Exit(2)
	}

	loadCompressedMap()
	excludeGlob := glob.MustCompile(exclude)
	filePaths := walkInputDir(excludeGlob)

	//
	concurrentGoroutines := make(chan struct{}, workers)
	var wg sync.WaitGroup
	for i, path := range filePaths {
		jobID := i + 1
		wg.Add(1)
		go func(jobID int, path string) {
			defer wg.Done()
			concurrentGoroutines <- struct{}{}
			if verbose {
				fmt.Println("Doing JobID: ", jobID)
			}
			compress(path)
			if verbose {
				fmt.Println("Finished JobID: ", jobID)
			}
			<-concurrentGoroutines
		}(jobID, path)
	}
	wg.Wait()
}

func usage() {
	fmt.Fprintf(os.Stderr, "usage: %s [OPTIONS]\n", filename)
	fmt.Fprintln(os.Stderr, "\nOPTIONS:")
	flag.PrintDefaults()
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "\nEXAMPLES:")
	fmt.Fprintf(os.Stderr, "  %s -input-dir images\n", filename)
	fmt.Fprintf(os.Stderr, "  %s -input-dir images -dryrun\n", filename)
	fmt.Fprintf(os.Stderr, "  %s -input-dir . -exclude .git\n", filename)
	fmt.Fprintf(os.Stderr, "  %s -input-dir . -exclude \"{*www*,*node_modules*}\"\n", filename)
	fmt.Fprintln(os.Stderr, "")
}

func loadCompressedMap() {
	file, err := os.Open(outputPath)
	if err != nil {
		if os.IsNotExist(err) {
			return
		}
		log.Fatal(err)
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		compressed[scanner.Text()] = struct{}{}
	}
	if err := scanner.Err(); err != nil {
		log.Fatal(err)
	}
}

func walkInputDir(excludeGlob glob.Glob) []string {
	filePaths := []string{}
	err := filepath.Walk(inputDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		// exclude hidden directories
		if info.IsDir() {
			dir := filepath.Base(path)
			if strings.HasPrefix(dir, ".") && len(dir) > 1 {
				if verbose {
					if dryRun {
						fmt.Print("(dryrun) ")
					}
					fmt.Printf("skipped %s because it's a hidden directory\n", dir)
				}
				return filepath.SkipDir
			}
		}

		// check exclude Glob for directories and images to skip
		slashpath := filepath.ToSlash(path)
		if excludeGlob != nil && excludeGlob.Match(slashpath) {
			if verbose {
				if dryRun {
					fmt.Print("(dryrun) ")
				}
				fmt.Printf("skipped %s because of Glob pattern passed to -exclude %q\n", filepath.Base(path), exclude)
			}
			// if the Glob matches as directory don't walk any further
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		// search path for images
		ext := strings.ToLower(filepath.Ext(path))
		if ext == ".jpg" || ext == ".png" {
			filePaths = append(filePaths, path)
		}
		return nil
	})
	if err != nil {
		log.Fatal(err)
	}
	return filePaths
}

func compress(path string) {
	ext := strings.ToLower(filepath.Ext(path))
	name := filepath.Base(path)

	//TODO: Get Original Size
	var size int64

	// get MD5 of image
	fileMD5, err := getFileMD5(path)
	if err != nil {
		log.Fatalf("error: failed to get MD5 of file: %s", name)
	}

	// skip if already compressed
	if _, ok := compressed[fileMD5]; ok {
		if verbose {
			if dryRun {
				fmt.Print("(dryrun) ")
			}
			fmt.Printf("skipped %s because it's already compressed\n", name)
		}
		return
	}

	// if not then compress and append new MD5 to file
	if dryRun {
		fmt.Printf("(dryrun) compressed: %s\n", name)
		return
	}

	if ext == ".jpg" {
		process(exec.Command("guetzli", "--quality", strconv.FormatInt(jpegQuality, 10), path, path))
	} else if ext == ".png" {
		process(exec.Command("zopflipng", "-m", "-y", path, path))
	} else {
		log.Fatal("error: file is not an image")
	}

	newMD5, err := getFileMD5(path)
	if err != nil {
		log.Fatalf("error: failed to get MD5 of file: %s", name)
	}

	fi, err := os.Stat(path)
	if err != nil {
		log.Fatalf("error: failed to get size of compressed image: %s", err)
	}

	prevSize := byteCountIEC(size)
	newSize := byteCountIEC(fi.Size())
	fmt.Printf("compressed: %s from: %s to: %s\n", name, prevSize, newSize)
	writeMD5toFile(newMD5)
}

func process(cmd *exec.Cmd) {
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = cmd.Stdout
	err := cmd.Run()
	if err != nil {
		fmt.Printf("error: compressing image: %s\n", out.String())
		log.Fatal(err)
	}
	if verbose {
		fmt.Printf(out.String())
	}
}

func getFileMD5(path string) (string, error) {
	var MD5 string
	file, err := os.Open(path)
	if err != nil {
		return MD5, err
	}
	defer file.Close()
	hash := md5.New()
	if _, err := io.Copy(hash, file); err != nil {
		return MD5, err
	}
	hashInBytes := hash.Sum(nil)[:16]
	MD5 = hex.EncodeToString(hashInBytes)
	return MD5, nil
}

func writeMD5toFile(fileMD5 string) {
	file, err := os.OpenFile(outputPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		log.Println(err)
	}
	defer file.Close()
	if _, err := file.WriteString(fileMD5 + "\n"); err != nil {
		log.Println(err)
	}
}

// convert a size in bytes to a human-readable string IEC (binary) format
// credit: https://yourbasic.org/golang/formatting-byte-size-to-human-readable-format/
func byteCountIEC(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB",
		float64(b)/float64(div), "KMGTPE"[exp])
}
