package main

import (
	"bufio"
	"encoding/json"
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
	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

const path = "/Users/lemmer/updog" // "/Volumes"

var currentDirs = make(map[string]bool)

type Config struct {
	Host     string `json:"host"`
	Port     string `json:"port"`
	Username string `json:"username"`
	Password string `json:"password"`
}

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

func uploadFile(path, filename string, conf *Config) {
	var auths []ssh.AuthMethod
	if aconn, err := net.Dial("unix", os.Getenv("SSH_AUTH_SOCK")); err == nil {
		auths = append(auths, ssh.PublicKeysCallback(agent.NewClient(aconn).Signers))

	}
	//	if *PASS != "" {
	auths = append(auths, ssh.Password(conf.Password))
	//	}

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

	c.MkdirAll(path)

	w, err := c.Create("/chiken.txt")
	if err != nil {
		log.Fatal(err)
	}
	w.Close()

	w, err = c.OpenFile("/chiken.txt", syscall.O_WRONLY)
	if err != nil {
		log.Fatal(err)
	}
	defer w.Close()

	f, err := os.Open("/Users/lemmer/updog/NO NAME/chiken.txt")
	if err != nil {
		log.Fatal(err)
	}
	defer f.Close()

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
	if n != size {
		log.Fatalf("copy: expected %v bytes, got %d", size, n)
	}
	log.Printf("wrote %v bytes in %s", size, time.Since(t1))
}

func main() {

	conf := LoadConfig()
	_ = conf
	fmt.Println(conf.Host)
	//ploadFile("/hohoho/chiken", "chiken", conf)

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

				//log.Printf("event %v:", event)

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

							out, err := exec.Command("cp", "-r", path+"/"+volume+"/", "/tmp/"+volume+"/").Output()
							if err != nil {
								fmt.Printf("%s", err)
							}
							_ = out

							dirs := make(map[string]bool)
							files := make(map[string]bool)

							err = filepath.Walk("/tmp/"+volume+"/", func(path string, info fs.FileInfo, err error) error {
								wg.Add(1)
								if err != nil {
									log.Printf("prevent panic by handling failure accessing a path %q: %v\n", path, err)
									return err
								}

								path = path[1:]
								//								path = path[strings.Index(path, "/"):]

								if info.Mode().IsDir() {
									dirs[path] = true
								} else {
									files[path] = true
								}

								wg.Done()

								return nil
							})
							log.Printf("something")

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
					log.Printf("Safe to remove USB drive")
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
		log.Fatal(err)
	}
	<-done
}

func getHostKey(host string) ssh.PublicKey {
	// parse OpenSSH known_hosts file
	// ssh or use ssh-keyscan to get initial key
	file, err := os.Open(filepath.Join(os.Getenv("HOME"), ".ssh", "known_hosts"))
	if err != nil {
		log.Fatal(err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	var hostKey ssh.PublicKey
	for scanner.Scan() {
		fields := strings.Split(scanner.Text(), " ")
		if len(fields) != 3 {
			continue
		}
		if strings.Contains(fields[0], host) {
			var err error
			hostKey, _, _, _, err = ssh.ParseAuthorizedKey(scanner.Bytes())
			if err != nil {
				log.Fatalf("error parsing %q: %v", fields[2], err)
			}
			break
		}
	}

	if hostKey == nil {
		log.Fatalf("no hostkey found for %s", host)
	}

	return hostKey
}
