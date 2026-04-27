package main

import (
	"bufio"
	"context"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

type LogCollector struct {
	clientset   kubernetes.Interface
	store       *Store
	interval    time.Duration
	maxPods     int
	log         *slog.Logger
	mu          sync.Mutex
	tailers     map[string]context.CancelFunc
	tailerCount int
	logCh       chan LogEntry
	dropped     atomic.Int64
}

func NewLogCollector(cs kubernetes.Interface, store *Store, interval time.Duration, maxPods int, log *slog.Logger) *LogCollector {
	return &LogCollector{
		clientset: cs,
		store:     store,
		interval:  interval,
		maxPods:   maxPods,
		log:       log,
		tailers:   make(map[string]context.CancelFunc),
		logCh:     make(chan LogEntry, 4096),
	}
}

func (c *LogCollector) Run(ctx context.Context) {
	go c.flushLoop(ctx)

	ticker := time.NewTicker(c.interval)
	defer ticker.Stop()

	c.sync(ctx)
	for {
		select {
		case <-ctx.Done():
			c.stopAll()
			return
		case <-ticker.C:
			c.sync(ctx)
		}
	}
}

func (c *LogCollector) flushLoop(ctx context.Context) {
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()

	buf := make([]LogEntry, 0, 256)
	for {
		select {
		case <-ctx.Done():
			c.drainAndFlush(buf)
			return
		case entry := <-c.logCh:
			buf = append(buf, entry)
			if len(buf) >= 256 {
				c.flushBatch(buf)
				buf = buf[:0]
			}
		case <-ticker.C:
			if len(buf) > 0 {
				c.flushBatch(buf)
				buf = buf[:0]
			}
			if n := c.dropped.Swap(0); n > 0 {
				c.log.Warn("log channel full, lines dropped", "count", n)
			}
		}
	}
}

func (c *LogCollector) flushBatch(entries []LogEntry) {
	if err := c.store.InsertLogs(entries); err != nil {
		c.log.Warn("failed to insert log batch", "count", len(entries), "error", err)
	}
}

func (c *LogCollector) drainAndFlush(buf []LogEntry) {
	for {
		select {
		case entry := <-c.logCh:
			buf = append(buf, entry)
		default:
			if len(buf) > 0 {
				c.flushBatch(buf)
			}
			return
		}
	}
}

func (c *LogCollector) sync(ctx context.Context) {
	nsList, err := c.clientset.CoreV1().Namespaces().List(ctx, metav1.ListOptions{})
	if err != nil {
		c.log.Error("failed to list namespaces", "error", err)
		return
	}

	activePods := map[string]bool{}

	for _, ns := range nsList.Items {
		if !strings.HasPrefix(ns.Name, "pj-") {
			continue
		}

		pods, err := c.clientset.CoreV1().Pods(ns.Name).List(ctx, metav1.ListOptions{
			LabelSelector: "app.kubernetes.io/name",
		})
		if err != nil {
			c.log.Warn("failed to list pods", "namespace", ns.Name, "error", err)
			continue
		}

		for _, pod := range pods.Items {
			key := ns.Name + "/" + pod.Name
			activePods[key] = true
			c.startTailer(ctx, ns.Name, &pod)
		}
	}

	c.mu.Lock()
	for key, cancel := range c.tailers {
		if !activePods[key] {
			cancel()
			delete(c.tailers, key)
			c.tailerCount--
		}
	}
	c.mu.Unlock()
}

func (c *LogCollector) startTailer(ctx context.Context, namespace string, pod *corev1.Pod) {
	key := namespace + "/" + pod.Name

	c.mu.Lock()
	if _, exists := c.tailers[key]; exists {
		c.mu.Unlock()
		return
	}
	if c.tailerCount >= c.maxPods {
		c.mu.Unlock()
		return
	}

	tailCtx, cancel := context.WithCancel(ctx)
	c.tailers[key] = cancel
	c.tailerCount++
	c.mu.Unlock()

	appName := pod.Labels["app.kubernetes.io/name"]
	envName := pod.Labels["mortise.dev/environment"]

	go c.tailPod(tailCtx, namespace, pod.Name, appName, envName)
}

func (c *LogCollector) tailPod(ctx context.Context, namespace, podName, app, env string) {
	tail := int64(100)
	opts := &corev1.PodLogOptions{
		Follow:     true,
		TailLines:  &tail,
		Timestamps: true,
	}

	stream, err := c.clientset.CoreV1().Pods(namespace).GetLogs(podName, opts).Stream(ctx)
	if err != nil {
		if ctx.Err() == nil {
			c.log.Warn("failed to open log stream", "pod", podName, "namespace", namespace, "error", err)
		}
		return
	}
	defer stream.Close()

	go func() {
		<-ctx.Done()
		stream.Close()
	}()

	scanner := bufio.NewScanner(stream)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		if ctx.Err() != nil {
			return
		}
		line := scanner.Text()
		ts, content := parseLogTimestamp(line)

		select {
		case c.logCh <- LogEntry{
			Ts:        ts,
			Pod:       podName,
			Namespace: namespace,
			App:       app,
			Env:       env,
			Stream:    "stdout",
			Line:      content,
		}:
		default:
			c.dropped.Add(1)
		}
	}
}

func (c *LogCollector) stopAll() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, cancel := range c.tailers {
		cancel()
	}
	clear(c.tailers)
	c.tailerCount = 0
}

func parseLogTimestamp(line string) (ts, content string) {
	idx := strings.IndexByte(line, ' ')
	if idx <= 0 {
		return time.Now().UTC().Format(time.RFC3339Nano), line
	}
	if _, err := time.Parse(time.RFC3339Nano, line[:idx]); err != nil {
		return time.Now().UTC().Format(time.RFC3339Nano), line
	}
	return line[:idx], line[idx+1:]
}
