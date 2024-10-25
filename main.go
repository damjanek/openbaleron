package main

import (
	"crypto/rand"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/gorilla/mux"

	probing "github.com/prometheus-community/pro-bing"
	log "github.com/sirupsen/logrus"
)

func main() {
	log.SetLevel(log.DebugLevel)
	log.SetFormatter(&log.TextFormatter{
		ForceColors:   true,
		FullTimestamp: true,
	})
	r := mux.NewRouter()
	r.HandleFunc("/icmp/{address:.*?}/{count:[0-9]+}/{size:[0-9]+}", icmp).Methods("GET")
	r.HandleFunc("/http/ping", httpPing).Methods("GET")
	r.HandleFunc("/http/serve/{size:[0-9]+}", httpServeContent).Methods("GET")
	r.HandleFunc("/http/{url:.*}", httpGet).Methods("GET")

	http.Handle("/", r)
	log.Info("Staring up the server")
	http.ListenAndServe(":60999", nil)
}

func httpGet(w http.ResponseWriter, req *http.Request) {
	params := mux.Vars(req)
	url := params["url"]
	log.Infof("HTTP Get: request to GET %s", url)
	client := http.Client{
		Timeout: 30 * time.Second,
	}
	res, err := client.Get(fmt.Sprintf("http://%s", url))
	if err != nil {
		log.Errorf("HTTP Get: %v", err)
		http.Error(w, fmt.Sprintf("Error: %v", err), http.StatusGone)
		return
	}
	if res.StatusCode != 200 {
		log.Errorf("HTTP Get: unexpected http response: %d", res.StatusCode)
		http.Error(w, fmt.Sprintf("Error: unexpected http response %d", res.StatusCode), res.StatusCode)
		return
	}
	// close response bodies for both failed and successful requests
	defer res.Body.Close()
	_, err = io.Copy(io.Discard, res.Body)
	if err != nil {
		log.Errorf("HTTP Get: %v", err)
		http.Error(w, fmt.Sprintf("Error: %v", err), http.StatusInternalServerError)
		return
	}
	fmt.Fprint(w, "ok\n")
}

func httpServeContent(w http.ResponseWriter, req *http.Request) {
	params := mux.Vars(req)
	respSize, err := strconv.Atoi(params["size"])
	if err != nil {
		log.Error(err)
		http.Error(w, fmt.Sprintf("Error: %v", err), http.StatusInternalServerError)
		return
	}
	log.Infof("HTTP Serve: got request for %dMB", respSize)
	size := int64(respSize * 1024 * 1024)
	_, err = io.CopyN(w, rand.Reader, size)
	if err != nil {
		log.Errorf("HTTP Serve: %v", err)
		http.Error(w, fmt.Sprintf("Error: %v", err), http.StatusInternalServerError)
		return
	}
}

func httpPing(w http.ResponseWriter, req *http.Request) {
	fmt.Fprint(w, "pong\n")
}

func icmp(w http.ResponseWriter, req *http.Request) {
	params := mux.Vars(req)
	count, err := strconv.Atoi(params["count"])
	if err != nil {
		log.Error(err)
		http.Error(w, fmt.Sprintf("Error: %v", err), http.StatusInternalServerError)
		return
	}
	size, err := strconv.Atoi(params["size"])
	if err != nil {
		log.Error(err)
		http.Error(w, fmt.Sprintf("Error: %v", err), http.StatusInternalServerError)
		return
	}
	err = icmpWrapper(params["address"], count, size)
	if err != nil {
		log.Errorf("ICMP: %s %d times with %d byte packets: %v", params["address"], count, size, err)
		http.Error(w, fmt.Sprintf("Error: %v", err), http.StatusGone)
		return
	}
	log.Info("ICMP: returning ok")
	fmt.Fprint(w, "ok\n")
}

func icmpWrapper(target string, count int, size int) error {
	log.Infof("ICMP: target %s, %d times with %d bytes", target, count, size)
	pinger, err := probing.NewPinger(target)
	if err != nil {
		return err
	}
	pinger.Size = size
	pinger.Interval = 100 * time.Millisecond
	pinger.Count = count
	pinger.Timeout = time.Duration(count) * 100 * time.Millisecond
	err = pinger.Run()
	if err != nil {
		return err
	}
	stats := pinger.Statistics()
	if stats.PacketLoss > 0 {
		return fmt.Errorf("loss: %0.2f%%", stats.PacketLoss)
	}

	return nil
}
