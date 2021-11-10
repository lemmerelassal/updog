package main

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"os/exec"
	"sync"

	"github.com/fsnotify/fsnotify"
)

const path = "/Users/lemmer/updog" // "/Volumes"

var currentDirs = make(map[string]bool)

type Config struct {
	Host     string `json:"host"`
	Port     uint16 `json:"port"`
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

func main() {

	conf := LoadConfig()
	_ = conf
	fmt.Println(conf.Host)

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

							out, err := exec.Command("cp", "-r", path+"/"+volume, "/tmp/"+volume).Output()
							if err != nil {
								fmt.Printf("%s", err)
							}
							_ = out

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
