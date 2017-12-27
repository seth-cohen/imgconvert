package main

import (
	"archive/zip"
	"fmt"
	"github.com/go-redis/redis"
	"github.com/gorilla/mux"
	"github.com/gorilla/websocket"
	"github.com/rs/xid"
	"html/template"
	"io"
	"io/ioutil"
	"log"
	"mime/multipart"
	"net/http"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
	"time"
)

var (
	homeDir  string
	upgrader = websocket.Upgrader{
		ReadBufferSize:  1024,
		WriteBufferSize: 1024,
	}
)

func main() {
	// Get the current user directory for tifig location
	usr, err := user.Current()
	if err != nil {
		log.Fatal(err)
	}
	homeDir = usr.HomeDir
	fmt.Println(homeDir)

	registerHandlers()
	http.ListenAndServe(":8081", nil)
}

func registerHandlers() {
	r := mux.NewRouter()
	r.HandleFunc("/convert", convertHandler)
	r.HandleFunc("/socket", socketHandler)
	r.HandleFunc("/download", downloadFileHandler)
	r.HandleFunc("/", index)
	http.Handle("/", r)
}

func handlePart(p *multipart.Part, txid string, ch chan<- []string) {
	if p.FormName() == "uploadfile" {
		fmt.Println("got a file: ", p)
		if filepath.Ext(p.FileName()) == ".HEIC" {
			fn := "/tmp/" + txid + "/" + p.FileName()
			if err := saveFile(p, fn); err != nil {
				log.Fatal("Save File: ", err)
			}
			ch <- []string{fn, strings.TrimSuffix(fn, filepath.Ext(fn)) + ".jpg"}
		}
	}
}

func saveFile(r io.Reader, fName string) error {
	// save file
	f, err := os.Create(fName)
	if err != nil {
		return err
	}
	defer f.Close()

	n, err := io.Copy(f, r)
	if err != nil {
		return err
	}
	fmt.Println("Bytes written: ", n)
	return nil
}

func convertFile(fileName string, out string) {
	fmt.Println("Converting File: ", fileName, " To: ", out)
	cmd := exec.Command(homeDir+"/Documents/Misc_Repos/tifig/build/tifig", fileName, out)

	tifigOut, err := cmd.Output()
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(tifigOut)
}

func createZip(fileName string, imgDir string) error {
	items, err := ioutil.ReadDir(imgDir)
	if err != nil {
		return fmt.Errorf("Error reading image directory")
	}
	files := []string{}
	for _, item := range items {
		if !item.IsDir() && filepath.Ext(item.Name()) != ".HEIC" {
			files = append(files, imgDir+"/"+item.Name())
		}
	}
	if len(files) == 0 {
		return fmt.Errorf("No files to zip")
	}

	newfile, err := os.Create(fileName)
	if err != nil {
		return err
	}
	defer newfile.Close()

	zipWriter := zip.NewWriter(newfile)
	defer zipWriter.Close()

	for _, file := range files {
		toZip, err := os.Open(file)
		if err != nil {
			return err
		}
		defer toZip.Close()

		// File info for header
		info, err := toZip.Stat()
		if err != nil {
			return err
		}

		header, err := zip.FileInfoHeader(info)
		if err != nil {
			return err
		}
		header.Method = zip.Deflate

		// Write the file to the zip
		writer, err := zipWriter.CreateHeader(header)
		if err != nil {
			return err
		}
		_, err = io.Copy(writer, toZip)
		if err != nil {
			return err
		}
	}

	return nil
}

// HTTP Handlers
func index(w http.ResponseWriter, r *http.Request) {
	t, _ := template.ParseFiles("templates/index.html")
	t.Execute(w, nil)
}

func convertHandler(w http.ResponseWriter, r *http.Request) {
	done := make(chan bool)
	ch := make(chan []string)
	toConvert := [][]string{}

	// DELETE
	key := xid.New()
	http.SetCookie(
		w,
		&http.Cookie{Name: "txid", Value: key.String(), Path: "/", Expires: time.Now().Add(10 * time.Minute)},
	)
	txid := key.String()

	// Go through all of the form parts
	go func(ch chan<- []string) {
		// Close, we are done writing to the channel once we exit this block
		defer close(ch)

		// Handle the multipart form
		mr, err := r.MultipartReader()
		if err != nil {
			log.Println("MultipartReader: ", err)
			return
		}
		for {
			part, err := mr.NextPart()
			if err == io.EOF {
				break
			} else if err != nil {
				log.Fatal(err)
			} else {
				// Note that we can't handle these in goroutine because calling NextPart() closes
				// the currently open Part
				err = os.MkdirAll("/tmp/"+txid, os.ModePerm)
				if err != nil {
					log.Fatal("Create Dir: ", err)
				}
				handlePart(part, txid, ch)
			}
		}
	}(ch)

	// Go once a file is written convert it
	go func(ch <-chan []string) {
		for msg := range ch {
			toConvert = append(toConvert, msg)
		}
		fmt.Println("No messages in channel")
		done <- true
	}(ch)

	// Wait until we are done processing files
	<-done

	go func(files [][]string) {
		redisClient := redis.NewClient(&redis.Options{
			Addr: "localhost:8079",
		})

		for _, file := range files {
			// convert file
			convertFile(file[0], file[1])

			redisClient.HMSet(txid, map[string]interface{}{
				file[1]: true,
			})
		}
		redisClient.HMSet(txid, map[string]interface{}{
			"complete": true,
		})

	}(toConvert)
}

func socketHandler(w http.ResponseWriter, r *http.Request) {
	keyCookie, err := r.Cookie("txid")
	if err != nil {
		log.Println("Socket Cookie: ", err)
	}

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Println(err)
		return
	}

	go writer(conn, keyCookie.Value)
	reader(conn)
}

func downloadFileHandler(w http.ResponseWriter, r *http.Request) {
	// get the txid from the request so we can grab the correct files to return to client
	keyCookie, err := r.Cookie("txid")
	if err != nil {
		log.Println("Socket Cookie: ", err)
	}

	// Zip all of the jpgs to return
	zipFN := "/tmp/" + keyCookie.Value + "/jpgs_" + string(time.Now().Unix()) + ".zip"
	err = createZip(zipFN, "/tmp/"+keyCookie.Value)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, "Error Converting Files")
		log.Println(err)
		return
	}

	// tell the browser the returned content should be downloaded
	w.Header().Add("Content-Disposition", "Attachment; filename=jpegs.zip")
	zFile, err := os.Open(zipFN)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		io.WriteString(w, "Error Converting Files")
		log.Println(err)
		return
	}
	defer zFile.Close()

	io.Copy(w, zFile)
}

// Websocket reading and writing
func reader(ws *websocket.Conn) {
	defer ws.Close()
	for {
		_, msg, err := ws.ReadMessage()
		if err != nil {
			break
		}

		// Print the message to the console
		fmt.Printf("%s sent: %s\n", ws.RemoteAddr(), string(msg))

	}

	fmt.Println("closing socket in Reader")
}

func writer(ws *websocket.Conn, key string) {
	redisClient := redis.NewClient(&redis.Options{
		Addr: "localhost:8079",
	})
	updateTicker := time.NewTicker(100 * time.Millisecond)
	defer updateTicker.Stop()

	for range updateTicker.C {
		// Write message back to browser
		hash := redisClient.HGetAll(key)
		if hash.Err() != nil {
			log.Println(hash.Err())
		}
		if err := ws.WriteJSON(hash.Val()); err != nil {
			fmt.Println("Error writing to socket")
			break
		}
	}

	fmt.Println("Closing socket in Writer")
}
