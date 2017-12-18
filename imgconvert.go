package main

import (
	"archive/zip"
	"fmt"
	"github.com/gorilla/mux"
	"html/template"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

func main() {
	cmd := exec.Command("date")
	dateOut, err := cmd.Output()
	if err != nil {
		log.Fatal(err)
	}

	fmt.Printf(string(dateOut))

	registerHandlers()

	http.ListenAndServe(":8081", nil)
}

func registerHandlers() {
	r := mux.NewRouter()
	r.HandleFunc("/convert", convertHandler)
	r.HandleFunc("/", index)
	http.Handle("/", r)
}

func index(w http.ResponseWriter, r *http.Request) {
	t, _ := template.ParseFiles("templates/index.html")
	t.Execute(w, nil)
}

func convertHandler(w http.ResponseWriter, r *http.Request) {
	done := make(chan bool)
	ch := make(chan []string)
	outFiles := []string{}

	fmt.Println("Content-Type: ", r.Header.Get("Content-Type"))
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
				handlePart(part, ch)
			}
		}
	}(ch)

	// Go once a file is written convert it
	go func(ch <-chan []string) {
		for msg := range ch {
			// convert file
			convertFile(msg[0], msg[1])
			outFiles = append(outFiles, msg[1])
		}
		fmt.Println("No messages in channel")
		done <- true
	}(ch)

	// Wait until we are done processing files
	<-done

	// Zip all of the jpgs to return
	zipFN := "/tmp/jpgs_" + string(time.Now().Unix()) + ".zip"
	err := createZip(zipFN, outFiles)
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

func handlePart(p *multipart.Part, ch chan<- []string) {
	if p.FormName() == "uploadfile" {
		fmt.Println("got a file: ", p)
		if filepath.Ext(p.FileName()) == ".HEIC" {
			fn := "/tmp/" + p.FileName()
			if err := saveFile(p, fn); err != nil {
				log.Fatal(err)
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
	cmd := exec.Command("/Users/scohen/Documents/Misc_Repos/tifig/build/tifig", fileName, out)

	tifigOut, err := cmd.Output()
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(tifigOut)
}

func createZip(fileName string, files []string) error {
	if len(files) == 0 {
		return fmt.Errorf("No files to convert")
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
