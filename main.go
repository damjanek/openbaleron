package main

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/mux"

	probing "github.com/prometheus-community/pro-bing"
	log "github.com/sirupsen/logrus"

	"github.com/redis/go-redis/v9"
)

var RedisClient *redis.Client

type Response struct {
	Hostname string `json:"hostname"`
	Output   string `json:"output"`
	Success  bool   `json:"success"`
}

type RunRequest struct {
	Command []string `json:"command"`
	Timeout int      `json:"timeout"`
	Cwd     string   `json:"cwd"`
}

type IcmpRequest struct {
	Address string `json:"address"`
	Count   int    `json:"count"`
	Size    int    `json:"size"`
	Burst   bool   `json:"burst"`
}

type HttpRequest struct {
	URL     string `json:"url"`
	Timeout int    `json:"timeout"`
}

type PutRequest struct {
	Location string `json:"location"`
	RedisKey string `json:"redis_key"`
	Mode     int    `json:"mode"`
}

func (r Response) MarshalBinary() ([]byte, error) {
	log.Infof("returning response: %+v", r)
	return json.Marshal(r)
}

// isGoodAddress checks if the address is a good one to listen on
func isGoodAddress(addr string) bool {
	banned := []string{
		"127.0.0",
		"fe80",
		"192.168.122",
		"172.17.0.1/16",
		"172.18.0.1/16",
		"::1/128",
		"/64",
	}
	for _, b := range banned {
		if strings.Contains(addr, b) {
			return false
		}
	}
	return true
}

// getAllIPs returns all non-loopback, non-docker, non-vibr IPs
func getAllIPs() (ret []string, err error) {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return []string{}, err
	}
	for _, addr := range addrs {
		// We want to skip any loopback addresses, vibr, and docker addresses
		if isGoodAddress(addr.String()) {
			ret = append(ret, addr.String())
		}
	}
	return ret, nil
}

func stripNetmask(ips []string) []string {
	var ret []string
	for _, ip := range ips {
		ret = append(ret, strings.Split(ip, "/")[0])
	}
	return ret
}

func icmpRequest(msg string) (Response, error) {
	var req IcmpRequest
	err := json.Unmarshal([]byte(msg), &req)
	if err != nil {
		return Response{
			Hostname: hostname(),
			Output:   fmt.Sprintf("malformed json: %v", err),
		}, fmt.Errorf("while unmarshalling ICMP request: %v", err)
	}
	ok := icmpWrapper(req.Address, req.Count, req.Size, req.Burst)
	if ok != nil {
		log.Errorf("icmpRequest not ok: %v", ok)
		return Response{
			Hostname: hostname(),
			Output:   fmt.Sprintf("%v", ok),
		}, nil
	}
	return Response{
		Hostname: hostname(),
		Output:   "",
		Success:  true,
	}, nil
}

func httpGet(msg string) (Response, error) {
	var req HttpRequest
	err := json.Unmarshal([]byte(msg), &req)
	if err != nil {
		return Response{
			Hostname: hostname(),
			Output:   fmt.Sprintf("malformed json: %v", err),
		}, fmt.Errorf("while unmarshalling HTTP GET request: %v", err)
	}
	client := http.Client{
		Timeout: 30 * time.Second,
	}
	if req.Timeout != 0 {
		client.Timeout = time.Duration(req.Timeout) * time.Second
	}
	// TODO: add support for custom headers
	res, err := client.Get(fmt.Sprintf("http://%s", req.URL))
	if err != nil {
		return Response{
			Hostname: hostname(),
			Output:   fmt.Sprintf("GET error: %v", err),
		}, fmt.Errorf("while running HTTP GET: %v", err)
	}
	if res.StatusCode != 200 {
		return Response{
			Hostname: hostname(),
			Output:   fmt.Sprintf("status code: %d", res.StatusCode),
		}, fmt.Errorf("unexpected http response: %d", res.StatusCode)
	}
	defer res.Body.Close()
	// We're discarding response anyways, so why would we care about the error?
	_, _ = io.Copy(io.Discard, res.Body)
	return Response{
		Hostname: hostname(),
		Output:   "",
		Success:  true,
	}, nil
}

func hostname() string {
	hostname, err := os.Hostname()
	if err != nil {
		return "unknown"
	}
	return hostname
}

func runRequest(msg string) (Response, error) {
	var req RunRequest
	err := json.Unmarshal([]byte(msg), &req)
	if err != nil {
		return Response{
			Hostname: hostname(),
			Output:   fmt.Sprintf("malformed json: %v", err),
		}, fmt.Errorf("while unmarshalling run request: %v", err)
	}
	if req.Timeout == 0 {
		req.Timeout = 10
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(req.Timeout)*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, req.Command[0], req.Command[1:]...)
	cmd.Dir = req.Cwd
	cmd.WaitDelay = 3 * time.Second
	out, err := cmd.CombinedOutput()
	if err != nil {
		log.Errorf("cmd: %+v, error: %v", cmd, err)
		return Response{
			Hostname: hostname(),
			Output:   fmt.Sprintf("error: %v", err),
		}, nil
	}
	return Response{
		Hostname: hostname(),
		Output:   string(out),
		Success:  true,
	}, nil
}

func put(msg string) (Response, error) {
	var req PutRequest
	err := json.Unmarshal([]byte(msg), &req)
	if err != nil {
		return Response{
			Hostname: hostname(),
			Output:   fmt.Sprintf("malformed json: %v", err),
		}, fmt.Errorf("while unmarshalling put request: %v", err)
	}
	file, err := os.Create(req.Location)
	if err != nil {
		return Response{
			Hostname: hostname(),
			Output:   fmt.Sprintf("error: %v", err),
		}, fmt.Errorf("while creating file: %v", err)
	}
	defer file.Close()
	content, err := RedisClient.Get(context.Background(), req.RedisKey).Result()
	if err != nil {
		os.Remove(req.Location)
		return Response{
			Hostname: hostname(),
			Output:   fmt.Sprintf("error: %v", err),
		}, fmt.Errorf("while getting content: %v", err)
	}
	_, err = file.WriteString(content)
	if err != nil {
		os.Remove(req.Location)
		return Response{
			Hostname: hostname(),
			Output:   fmt.Sprintf("error: %v", err),
		}, fmt.Errorf("while writing content: %v", err)
	}

	return Response{
		Hostname: hostname(),
		Output:   "",
		Success:  true,
	}, nil
}

func parseMessage(channel, workID string) (Response, error) {
	log.Infof("channel: %s, message: %s", channel, workID)
	msg, err := RedisClient.Get(context.Background(), workID).Result()
	if err == redis.Nil {
		return Response{}, fmt.Errorf("workID %s not found", workID)
	}
	log.Debug(msg)
	switch {
	case strings.HasPrefix(workID, "icmp"):
		return icmpRequest(msg)
	case strings.HasPrefix(workID, "httpget"):
		return httpGet(msg)
	case strings.HasPrefix(workID, "put"):
		return put(msg)
	case strings.HasPrefix(workID, "run"):
		return runRequest(msg)
	case workID == "shutdown":
		log.Info("shutting down as requested")
		os.Exit(0)
		return Response{}, nil
	default:
		log.Info("unsupported")
		return Response{
			Hostname: hostname(),
			Output:   fmt.Sprintf("unsupported task type: %s", workID),
			Success:  false,
		}, fmt.Errorf("unsupported task type: %s", workID)
	}
}

func getChannels() ([]string, error) {
	channels, err := getAllIPs()
	if err != nil {
		return []string{}, fmt.Errorf("while getting IPs: %v", err)
	}
	channels = stripNetmask(channels)
	hostname, err := os.Hostname()
	if err != nil {
		return []string{}, fmt.Errorf("while getting hostname: %v", err)
	}
	channels = append(channels, hostname)
	return channels, nil
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

func icmpWrapper(target string, count int, size int, burst bool) error {
	log.Infof("ICMP: target %s, %d times with %d bytes, burst: %v", target, count, size, burst)
	pinger, err := probing.NewPinger(target)
	if err != nil {
		return err
	}
	pinger.Size = size
	pinger.Interval = 1000 * time.Millisecond
	pinger.Timeout = time.Duration(count) * 1000 * time.Millisecond
	if burst {
		pinger.Interval = 100 * time.Millisecond
		pinger.Timeout = time.Duration(count) * 100 * time.Millisecond
	}
	pinger.Interval = 100 * time.Millisecond
	pinger.Count = count
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

func main() {
	var httpPort = flag.Int("p", 60999, "Port used for HTTP server")
	var redisHost = flag.String("r", "169.254.1.1:6379", "Redis host (host:port)")
	flag.Parse()

	log.SetLevel(log.InfoLevel)
	log.SetFormatter(&log.TextFormatter{
		ForceColors:   true,
		FullTimestamp: true,
	})
	// Setup a HTTP server that will be used for serving content
	r := mux.NewRouter()
	r.HandleFunc("/http/serve/{size:[0-9]+}", httpServeContent).Methods("GET")
	listenAddr := fmt.Sprintf("0.0.0.0:%d", *httpPort)
	srv := &http.Server{
		Addr:         listenAddr,
		WriteTimeout: time.Second * 15,
		ReadTimeout:  time.Second * 15,
		IdleTimeout:  time.Second * 60,
		Handler:      r,
	}
	go func() {
		if err := srv.ListenAndServe(); err != nil {
			log.Error(err)
		}
	}()
	log.Infof("HTTP server listening on %s", listenAddr)
	// Redis setup
	ctx := context.Background()
	RedisClient = redis.NewClient(&redis.Options{
		Addr:     *redisHost,
		Password: "",
		DB:       0,
	})
	channels, err := getChannels()
	if err != nil {
		log.Fatalf("while getting channels: %v", err)
	}
	log.Infof("subscribing to channels: %v", channels)
	pubSub := RedisClient.Subscribe(
		ctx,
		channels...,
	)
	// main receiving loop
	for {
		msg, err := pubSub.ReceiveMessage(ctx)
		if err != nil {
			log.Error(err)
			continue
		}
		ret, err := parseMessage(msg.Channel, msg.Payload)
		if err != nil {
			log.Error(err)
		}
		err = RedisClient.Publish(ctx, "output", ret).Err()
		if err != nil {
			log.Errorf("publish: %v", err)
		}
	}
}
