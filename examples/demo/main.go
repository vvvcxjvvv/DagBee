package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math/rand"
	"net/http"
	"strings"
	"sync"
	"time"

	"dagbee"
)

// ============================================================
// Data Structures
// ============================================================

type DAGInfo struct {
	Name  string     `json:"name"`
	Nodes []NodeInfo `json:"nodes"`
	Edges []EdgeInfo `json:"edges"`
}

type NodeInfo struct {
	Name      string   `json:"name"`
	DependsOn []string `json:"dependsOn"`
	Priority  int      `json:"priority"`
}

type EdgeInfo struct {
	From string `json:"from"`
	To   string `json:"to"`
}

type RunResult struct {
	Status   string            `json:"status"`
	Duration string            `json:"duration"`
	Nodes    map[string]string `json:"nodes"`
	Logs     []string          `json:"logs"`
}

// ============================================================
// Global State
// ============================================================

var (
	mu              sync.Mutex
	logs            []string
	latestDagResult *dagbee.DagResult
)

func addLog(format string, args ...interface{}) {
	mu.Lock()
	defer mu.Unlock()
	msg := fmt.Sprintf(format, args...)
	logs = append(logs, msg)
	if len(logs) > 200 {
		logs = logs[len(logs)-100:]
	}
	fmt.Println(msg)
}

// ============================================================
// DAG Definition
// ============================================================

func buildDAG() *dagbee.DAG {
	d := dagbee.NewDAG("data-pipeline",
		dagbee.WithMaxConcurrency(8),
		dagbee.WithTimeout(30*time.Second),
		dagbee.WithLogger(dagbee.NewStdLogger()),
	)

	// Node 1: Fetch Data
	d.AddNode("Fetch", func(ctx context.Context, store *dagbee.SharedStore) error {
		addLog("[Fetch] 开始获取数据...")
		time.Sleep(time.Duration(1500+rand.Intn(1000)) * time.Millisecond)

		rawData := []string{"record_1", "record_2", "record_3", "record_4", "record_5", "record_6"}
		store.Set("raw_data", rawData)
		store.Set("fetch_count", len(rawData))

		addLog("[Fetch] 完成! 获取到 %d 条数据", len(rawData))
		return nil
	}, dagbee.NodeWithPriority(10))

	// Node 2-4: Parse in parallel
	d.AddNode("Parse_A", func(ctx context.Context, store *dagbee.SharedStore) error {
		addLog("[Parse_A] 开始解析数据分片 A...")
		time.Sleep(time.Duration(1000+rand.Intn(1000)) * time.Millisecond)

		raw, _ := dagbee.GetTyped[[]string](store, "raw_data")
		parsed := make([]string, 0)
		for _, r := range raw {
			if strings.HasPrefix(r, "record_1") || strings.HasPrefix(r, "record_2") {
				parsed = append(parsed, "parsed_A_"+r)
			}
		}
		store.Set("parsed_a", parsed)
		addLog("[Parse_A] 完成! 解析了 %d 条记录", len(parsed))
		return nil
	}, dagbee.NodeWithDependsOn("Fetch"), dagbee.NodeWithPriority(5))

	d.AddNode("Parse_B", func(ctx context.Context, store *dagbee.SharedStore) error {
		addLog("[Parse_B] 开始解析数据分片 B...")
		time.Sleep(time.Duration(1200+rand.Intn(800)) * time.Millisecond)

		raw, _ := dagbee.GetTyped[[]string](store, "raw_data")
		parsed := make([]string, 0)
		for _, r := range raw {
			if strings.HasPrefix(r, "record_3") || strings.HasPrefix(r, "record_4") {
				parsed = append(parsed, "parsed_B_"+r)
			}
		}
		store.Set("parsed_b", parsed)
		addLog("[Parse_B] 完成! 解析了 %d 条记录", len(parsed))
		return nil
	}, dagbee.NodeWithDependsOn("Fetch"), dagbee.NodeWithPriority(5))

	d.AddNode("Parse_C", func(ctx context.Context, store *dagbee.SharedStore) error {
		addLog("[Parse_C] 开始解析数据分片 C...")
		time.Sleep(time.Duration(800+rand.Intn(1200)) * time.Millisecond)

		raw, _ := dagbee.GetTyped[[]string](store, "raw_data")
		parsed := make([]string, 0)
		for _, r := range raw {
			if strings.HasPrefix(r, "record_5") || strings.HasPrefix(r, "record_6") {
				parsed = append(parsed, "parsed_C_"+r)
			}
		}
		store.Set("parsed_c", parsed)
		addLog("[Parse_C] 完成! 解析了 %d 条记录", len(parsed))
		return nil
	}, dagbee.NodeWithDependsOn("Fetch"), dagbee.NodeWithPriority(5))

	// Node 5: Merge
	d.AddNode("Merge", func(ctx context.Context, store *dagbee.SharedStore) error {
		addLog("[Merge] 开始合并结果...")
		time.Sleep(time.Duration(1000+rand.Intn(500)) * time.Millisecond)

		a, _ := dagbee.GetTyped[[]string](store, "parsed_a")
		b, _ := dagbee.GetTyped[[]string](store, "parsed_b")
		c, _ := dagbee.GetTyped[[]string](store, "parsed_c")

		all := make([]string, 0, len(a)+len(b)+len(c))
		all = append(all, a...)
		all = append(all, b...)
		all = append(all, c...)

		store.Set("merged_data", all)
		addLog("[Merge] 完成! 共合并 %d 条记录", len(all))
		return nil
	}, dagbee.NodeWithDependsOn("Parse_A", "Parse_B", "Parse_C"), dagbee.NodeWithPriority(8))

	// Node 6: Validate
	d.AddNode("Validate", func(ctx context.Context, store *dagbee.SharedStore) error {
		addLog("[Validate] 开始验证数据...")
		time.Sleep(time.Duration(500+rand.Intn(500)) * time.Millisecond)

		merged, _ := dagbee.GetTyped[[]string](store, "merged_data")
		valid := 0
		for _, item := range merged {
			if strings.HasPrefix(item, "parsed_") {
				valid++
			}
		}
		store.Set("valid_count", valid)
		store.Set("total_count", len(merged))

		addLog("[Validate] 完成! %d/%d 条记录通过验证", valid, len(merged))
		return nil
	}, dagbee.NodeWithDependsOn("Merge"), dagbee.NodeWithPriority(7))

	// Node 7: Output
	d.AddNode("Output", func(ctx context.Context, store *dagbee.SharedStore) error {
		addLog("[Output] 开始输出最终结果...")
		time.Sleep(time.Duration(500+rand.Intn(500)) * time.Millisecond)

		valid, _ := dagbee.GetTyped[int](store, "valid_count")
		total, _ := dagbee.GetTyped[int](store, "total_count")

		result := fmt.Sprintf("Pipeline Complete: %d/%d records processed successfully", valid, total)
		store.Set("final_result", result)

		addLog("[Output] 完成! 最终结果: %s", result)
		return nil
	}, dagbee.NodeWithDependsOn("Validate"), dagbee.NodeWithPriority(5))

	return d
}

// ============================================================
// DAG Info (static topology)
// ============================================================

func getDAGInfo() DAGInfo {
	nodeDefs := []struct {
		name      string
		dependsOn []string
		priority  int
	}{
		{"Fetch", nil, 10},
		{"Parse_A", []string{"Fetch"}, 5},
		{"Parse_B", []string{"Fetch"}, 5},
		{"Parse_C", []string{"Fetch"}, 5},
		{"Merge", []string{"Parse_A", "Parse_B", "Parse_C"}, 8},
		{"Validate", []string{"Merge"}, 7},
		{"Output", []string{"Validate"}, 5},
	}

	info := DAGInfo{
		Name:  "data-pipeline",
		Nodes: make([]NodeInfo, 0, len(nodeDefs)),
		Edges: make([]EdgeInfo, 0),
	}

	for _, nd := range nodeDefs {
		info.Nodes = append(info.Nodes, NodeInfo{
			Name:      nd.name,
			DependsOn: nd.dependsOn,
			Priority:  nd.priority,
		})
		for _, dep := range nd.dependsOn {
			info.Edges = append(info.Edges, EdgeInfo{From: dep, To: nd.name})
		}
	}

	return info
}

// ============================================================
// HTTP Handlers
// ============================================================

func handleDAGInfo(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	json.NewEncoder(w).Encode(getDAGInfo())
}

func handleRun(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	mu.Lock()
	logs = make([]string, 0)
	mu.Unlock()

	addLog("========================================")
	addLog("开始执行数据管道...")
	addLog("========================================")

	d := buildDAG()

	startTime := time.Now()
	result := dagbee.NewEngine().Run(context.Background(), d)
	elapsed := time.Since(startTime)

	mu.Lock()
	latestDagResult = result
	mu.Unlock()

	addLog("========================================")
	addLog("管道执行完成! 总耗时: %s", elapsed.Round(time.Millisecond))
	addLog("========================================")

	nodeStatuses := make(map[string]string)
	nodeNames := []string{"Fetch", "Parse_A", "Parse_B", "Parse_C", "Merge", "Validate", "Output"}
	for _, name := range nodeNames {
		nr := result.NodeResult(name)
		if nr != nil {
			nodeStatuses[name] = nr.Status.String()
		} else {
			nodeStatuses[name] = "UNKNOWN"
		}
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	json.NewEncoder(w).Encode(RunResult{
		Status:   result.Status.String(),
		Duration: elapsed.String(),
		Nodes:    nodeStatuses,
		Logs:     logs,
	})
}

func handleLogs(w http.ResponseWriter, r *http.Request) {
	mu.Lock()
	defer mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"logs": logs,
	})
}

// ============================================================
// Main
// ============================================================

func main() {
	port := ":8080"

	// API routes
	http.HandleFunc("/api/dag", handleDAGInfo)
	http.HandleFunc("/api/run", handleRun)
	http.HandleFunc("/api/logs", handleLogs)

	// Serve static frontend files
	http.Handle("/", http.FileServer(http.Dir("./")))

	fmt.Printf("🚀 DagBee Demo 启动于 http://localhost%s\n", port)
	fmt.Println("   在浏览器中打开即可查看 DAG 可视化界面")
	log.Fatal(http.ListenAndServe(port, nil))
}
