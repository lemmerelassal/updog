package main

import (
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

const path = "/Volumes" // "/Users/lemmer/updog" // "/Volumes"
const tmpdir = "/tmp"   // "/Users/lemmer/updog/tmp"

var currentDirs = make(map[string]bool)

type Config struct {
	Host     string `json:"host"`
	Port     string `json:"port"`
	Username string `json:"username"`
	Password string `json:"password"`
	//RetryCount string `json: "retrycount"`
}

// load config from json file

func LoadConfig() *Config {
	file, err := os.Open("conf.json")

	if err != nil {
		panic(err)
	}

	defer file.Close()

	config := &Config{}
	decoder := json.NewDecoder(file)
	err = decoder.Decode(config)

	if err != nil {
		panic(err)
	}

	return config
}

// uploadFiles parameters:
//	files: path relative to tmpdir; true = file, false = dir
//	conf: SFTP parameters
func uploadFiles(files map[string]bool, conf *Config) (map[string]bool, error) {
	var auths []ssh.AuthMethod
	if aconn, err := net.Dial("unix", os.Getenv("SSH_AUTH_SOCK")); err == nil {
		auths = append(auths, ssh.PublicKeysCallback(agent.NewClient(aconn).Signers))

	}

	auths = append(auths, ssh.Password(conf.Password))

	config := ssh.ClientConfig{
		User:            conf.Username,
		Auth:            auths,
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
	}
	addr := fmt.Sprintf("%s:%s", conf.Host, conf.Port)
	conn, err := ssh.Dial("tcp", addr, &config)
	if err != nil {
		log.Fatalf("unable to connect to [%s]: %v", addr, err)
	}
	defer conn.Close()

	c, err := sftp.NewClient(conn, sftp.MaxPacket(1<<15))
	if err != nil {
		log.Fatalf("unable to start sftp subsytem: %v", err)
	}
	defer c.Close()

	for path, isFile := range files {
		if !isFile {
			c.MkdirAll(path)
		}
	}

	newError := ""

	remainingFiles := make(map[string]bool)

	for path, isFile := range files {
		if isFile {

			w, err := c.Create(path)
			if err != nil {
				log.Fatal(err)
			}
			w.Close()

			w, err = c.OpenFile(path, syscall.O_WRONLY)
			if err != nil {
				log.Fatal(err)
			}

			f, err := os.Open(tmpdir + "/" + path)
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
				os.Remove(tmpdir + "/" + path)
			}

		}
	}
	if len(newError) > 0 {
		return remainingFiles, errors.New(newError)
	}
	return remainingFiles, nil
}

func main() {

	conf := LoadConfig()

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		log.Fatal(err)
	}
	defer watcher.Close()

	files, err := ioutil.ReadDir(path)

	if err != nil {

		log.Fatal(err)
	}

	currentDirs := make(map[string]bool)
	for _, f := range files {
		if f.IsDir() {

			currentDirs[f.Name()] = true

		}

	}

	done := make(chan bool)
	go func() {
		for {

			select {

			case event, ok := <-watcher.Events:
				_ = event
				if !ok {
					return
				}

				// add wait here until drive is ready
				timer := time.NewTimer(1 * time.Second)
				<-timer.C
				timer.Stop()

				files, err := ioutil.ReadDir(path)

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

							src := path + "/" + volume + "/"
							dest := tmpdir + "/" + newuuid + "/"

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
								files, err = uploadFiles(files, conf)
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

	err = watcher.Add(path)
	if err != nil {
		log.Printf("watcher error: %v", err)
	}
	<-done
}
