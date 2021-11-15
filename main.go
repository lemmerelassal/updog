package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"io/ioutil"
	"log"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/google/uuid"
	"github.com/otiai10/copy"
	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

type config struct {
	Host     string `json:"host"`
	Port     string `json:"port"`
	Username string `json:"username"`
	Password string `json:"password"`
	Path     string `json:"path"`
	Tmpdir   string `json:"tmpdir"`
}

func (c *config) init(args []string) error {

	file, err := os.Open("/etc/updog.json")

	if err != nil {
		return err
	}

	defer file.Close()

	config := &config{}
	decoder := json.NewDecoder(file)
	err = decoder.Decode(config)

	if err != nil {
		return err
	}

	c.Host = config.Host
	c.Password = config.Password
	c.Port = config.Port
	c.Username = config.Username
	c.Tmpdir = config.Tmpdir
	c.Path = config.Path

	return nil

}

// uploadFiles parameters:
//	files: path relative to tmpdir; true = file, false = dir
//	conf: SFTP parameters
func (c *config) uploadFiles(files map[string]bool) (map[string]bool, error) {
	var auths []ssh.AuthMethod
	if aconn, err := net.Dial("unix", os.Getenv("SSH_AUTH_SOCK")); err == nil {
		auths = append(auths, ssh.PublicKeysCallback(agent.NewClient(aconn).Signers))

	}

	auths = append(auths, ssh.Password(c.Password))

	config := ssh.ClientConfig{
		User:            c.Username,
		Auth:            auths,
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
	}
	addr := fmt.Sprintf("%s:%s", c.Host, c.Port)
	conn, err := ssh.Dial("tcp", addr, &config)
	if err != nil {
		log.Fatalf("unable to connect to [%s]: %v", addr, err)
	}
	defer conn.Close()

	client, err := sftp.NewClient(conn, sftp.MaxPacket(1<<15))
	if err != nil {
		log.Fatalf("unable to start sftp subsytem: %v", err)
	}
	defer client.Close()

	for path, isFile := range files {
		if !isFile {
			client.MkdirAll(path)
		}
	}

	newError := ""

	remainingFiles := make(map[string]bool)

	for path, isFile := range files {
		if isFile {

			w, err := client.Create(path)
			if err != nil {
				log.Fatal(err)
			}
			w.Close()

			w, err = client.OpenFile(path, syscall.O_WRONLY)
			if err != nil {
				log.Fatal(err)
			}

			f, err := os.Open(c.Tmpdir + "/" + path)
			if err != nil {
				log.Fatal(err)
			}

			stat, err := f.Stat()
			if err != nil {
				log.Fatal(err)
			}
			size := stat.Size()

			log.Printf("writing %v bytes", size)
			t1 := time.Now()
			n, err := io.Copy(w, io.LimitReader(f, int64(size)))
			if err != nil {
				log.Fatal(err)
			}

			w.Close()
			f.Close()

			if n != size {
				if len(newError) > 0 {
					newError += "\n"
				}

				newError += fmt.Sprintf("%s: expected %v bytes, got %d", path, size, n)
				remainingFiles[path] = files[path]

			} else {
				log.Printf("wrote %v bytes in %s", size, time.Since(t1))
				os.Remove(c.Tmpdir + "/" + path)
			}

		}
	}
	if len(newError) > 0 {
		return remainingFiles, errors.New(newError)
	}
	return remainingFiles, nil
}

func main() {
	ctx := context.Background()
	ctx, cancel := context.WithCancel(ctx)

	signalChan := make(chan os.Signal, 1)
	signal.Notify(signalChan, os.Interrupt, syscall.SIGHUP)

	c := &config{}

	defer func() {
		signal.Stop(signalChan)
		cancel()
	}()

	go func() {
		for {
			select {
			case s := <-signalChan:
				switch s {
				case syscall.SIGHUP:
					c.init(os.Args)
				case os.Interrupt:
					cancel()
					os.Exit(1)
				}
			case <-ctx.Done():
				log.Printf("Done.")
				os.Exit(1)
			}
		}
	}()

	if err := run(ctx, c, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "%s\n", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, c *config, stdout io.Writer) error {
	c.init(os.Args)
	log.SetOutput(os.Stdout)

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	defer watcher.Close()

	files, err := ioutil.ReadDir(c.Path)

	if err != nil {

		return err
	}

	currentDirs := make(map[string]bool)
	for _, f := range files {
		if f.IsDir() {

			currentDirs[f.Name()] = true

		}

	}

	done := make(chan error)
	go func() {
		for {

			select {

			case <-ctx.Done():
				done <- nil

			case event, ok := <-watcher.Events:
				_ = event
				if !ok {
					return
				}

				// add wait here until drive is ready
				timer := time.NewTimer(1 * time.Second)
				<-timer.C
				timer.Stop()

				files, err := ioutil.ReadDir(c.Path)

				if err != nil {

					log.Fatal(err)
				}

				tempDirs := make(map[string]bool)
				for _, f := range files {
					if f.IsDir() {

						tempDirs[f.Name()] = true

					}

				}

				// detect incremental changes in directory listing

				hasIncremental := false

				wg := &sync.WaitGroup{}

				for k := range tempDirs {
					if _, ok := currentDirs[k]; !ok {

						log.Printf("\nNew volume: %s", k)
						wg.Add(1)
						hasIncremental = true

						go func(volume string) {

							//lwg := &sync.WaitGroup{}

							newuuid := uuid.New().String()

							src := c.Path + "/" + volume + "/"
							dest := c.Tmpdir + "/" + newuuid + "/"

							err := os.MkdirAll(dest, 0777)

							if err != nil {
								fmt.Printf("%s", err)

							}

							log.Printf("source: %s\ndest: %s", src, dest)

							err = copy.Copy(src, dest)
							if err != nil {
								log.Printf("error copying usb stick: %v", err)
							}

							volpath, err := exec.Command("bash", "-c", "df | grep \""+volume+"\"").Output()
							if err != nil {
								log.Printf("df errror %v", err)
							}

							volpath = volpath[:strings.Index(string(volpath), " ")]
							fmt.Printf("The volume path is %s\n", volpath)

							out, err := exec.Command("diskutil", "umount", string(volpath)).Output()
							if err != nil {
								log.Printf("diskutil umount error: %v", err)
							}
							log.Printf("%s", out)

							files := make(map[string]bool)

							err = filepath.Walk(dest, func(fpath string, info fs.FileInfo, err error) error {

								if err != nil {
									log.Printf("prevent panic by handling failure accessing a path %q: %v\n", fpath, err)
									return err
								}

								idx := strings.Index(fpath, dest)

								fpath = fpath[idx+len(dest):]

								if len(fpath) > 0 {

									files[newuuid+"/"+fpath] = !info.Mode().IsDir()

								}

								return nil
							})
							if err != nil {
								log.Printf("filepath.Walk error: %v\n", err)
							}

							doneUploading := false
							for !doneUploading {
								files, err = c.uploadFiles(files)
								doneUploading = true
								if err != nil {
									log.Printf("error uploading files: %v", err)
									doneUploading = false
								}
							}

							wg.Done()

						}(k)

					}

				}

				// detect decremental changes in directory listing

				for k := range currentDirs {
					if _, ok := tempDirs[k]; !ok {

						log.Printf("\nVolume removed: %s", k)

					}

				}

				currentDirs = tempDirs

				if hasIncremental {
					wg.Wait()
					/* 					err := beeep.Notify("Title", "Safe to remove USB drive", "assets/information.png")
					   					if err != nil {
					   						log.Printf("error in beeep: %v", err)
					   					}
					   					log.Printf("Safe to remove USB drive") */
				}

			case err, ok := <-watcher.Errors:
				if !ok {
					return
				}
				log.Println("error:", err)
			}

		}
	}()

	err = watcher.Add(c.Path)
	if err != nil {
		log.Printf("watcher error: %v", err)
	}
	err = <-done
	return err

}
