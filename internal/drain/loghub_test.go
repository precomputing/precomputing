package drain

import (
	"bufio"
	"encoding/csv"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The benchmark settings of logpai/logparser (logparser/Drain/benchmark.py) and the accuracy its
// README publishes for Drain on the Loghub 2,000-line samples.
var loghub = []struct {
	name, format string
	masks        []string
	st           float64
	depth        int
	accuracy     float64
}{
	{"HDFS", `<Date> <Time> <Pid> <Level> <Component>: <Content>`, []string{`blk_-?\d+`, `(\d+\.){3}\d+(:\d+)?`}, 0.5, 4, 0.9975},
	{"Hadoop", `<Date> <Time> <Level> \[<Process>\] <Component>: <Content>`, []string{`(\d+\.){3}\d+`}, 0.5, 4, 0.9475},
	{"Spark", `<Date> <Time> <Level> <Component>: <Content>`, []string{`(\d+\.){3}\d+`, `\b[KGTM]?B\b`, `([\w-]+\.){2,}[\w-]+`}, 0.5, 4, 0.92},
	{"Zookeeper", `<Date> <Time> - <Level>  \[<Node>:<Component>@<Id>\] - <Content>`, []string{`(/|)(\d+\.){3}\d+(:\d+)?`}, 0.5, 4, 0.9665},
	{"BGL", `<Label> <Timestamp> <Date> <Node> <Time> <NodeRepeat> <Type> <Component> <Level> <Content>`, []string{`core\.\d+`}, 0.5, 4, 0.9625},
	{"HPC", `<LogId> <Node> <Component> <State> <Time> <Flag> <Content>`, []string{`=\d+`}, 0.5, 4, 0.887},
	{"Thunderbird", `<Label> <Timestamp> <Date> <User> <Month> <Day> <Time> <Location> <Component>(\[<PID>\])?: <Content>`, []string{`(\d+\.){3}\d+`}, 0.5, 4, 0.955},
	{"Windows", `<Date> <Time>, <Level>                  <Component>    <Content>`, []string{`0x.*?\s`}, 0.7, 5, 0.997},
	{"Linux", `<Month> <Date> <Time> <Level> <Component>(\[<PID>\])?: <Content>`, []string{`(\d+\.){3}\d+`, `\d{2}:\d{2}:\d{2}`}, 0.39, 6, 0.69},
	{"Android", `<Date> <Time>  <Pid>  <Tid> <Level> <Component>: <Content>`, []string{`(/[\w-]+)+`, `([\w-]+\.){2,}[\w-]+`, `\b(\-?\+?\d+)\b|\b0[Xx][a-fA-F\d]+\b|\b[a-fA-F\d]{4,}\b`}, 0.2, 6, 0.911},
	{"HealthApp", `<Time>\|<Component>\|<Pid>\|<Content>`, nil, 0.2, 4, 0.78},
	{"Apache", `\[<Time>\] \[<Level>\] <Content>`, []string{`(\d+\.){3}\d+`}, 0.5, 4, 1},
	{"Proxifier", `\[<Time>\] <Program> - <Content>`, []string{`<\d+\ssec`, `([\w-]+\.)+[\w-]+(:\d+)?`, `\d{2}:\d{2}(:\d{2})*`, `[KGTM]B`}, 0.6, 3, 0.5265},
	{"OpenSSH", `<Date> <Day> <Time> <Component> sshd\[<Pid>\]: <Content>`, []string{`(\d+\.){3}\d+`, `([\w-]+\.){2,}[\w-]+`}, 0.6, 5, 0.7875},
	{"OpenStack", `<Logrecord> <Date> <Time> <Pid> <Level> <Component> \[<ADDR>\] <Content>`, []string{`((\d+\.){3}\d+,?)+`, `/.+?\s`, `\d+`}, 0.5, 5, 0.7325},
	{"Mac", `<Month>  <Date> <Time> <User> <Component>\[<PID>\]( \(<Address>\))?: <Content>`, []string{`([\w-]+\.){2,}[\w-]+`}, 0.7, 6, 0.7865},
}

// formatRegex turns a log format into a regular expression as the reference does: each <Field>
// becomes a lazy named group, the text between fields is a regular expression, and runs of spaces
// match any run of spaces.
func formatRegex(format string) (*regexp.Regexp, error) {
	spaces := regexp.MustCompile(` +`)
	field := regexp.MustCompile(`<[^<>]+>`)
	var b strings.Builder
	b.WriteString("^")
	last := 0
	for _, m := range field.FindAllStringIndex(format, -1) {
		b.WriteString(spaces.ReplaceAllString(format[last:m[0]], `\s+`))
		b.WriteString("(?P<" + strings.Trim(format[m[0]:m[1]], "<>") + ">.*?)")
		last = m[1]
	}
	b.WriteString(spaces.ReplaceAllString(format[last:], `\s+`))
	b.WriteString("$")
	return regexp.Compile(b.String())
}

// accuracy is logparser's grouping accuracy: a message is parsed correctly when its group holds
// exactly the messages of its true group. It also returns the pairwise F1 measure.
func accuracy(truth, parsed []string) (acc, f1 float64) {
	comb2 := func(n int) float64 { return float64(n) * float64(n-1) / 2 }
	truthSize := map[string]int{}
	for _, t := range truth {
		truthSize[t]++
	}
	groups := map[string][]int{}
	var order []string
	for i, p := range parsed {
		if _, ok := groups[p]; !ok {
			order = append(order, p)
		}
		groups[p] = append(groups[p], i)
	}
	var realPairs, parsedPairs, accuratePairs float64
	for _, n := range truthSize {
		if n > 1 {
			realPairs += comb2(n)
		}
	}
	correct := 0
	for _, p := range order {
		ids := groups[p]
		if len(ids) > 1 {
			parsedPairs += comb2(len(ids))
		}
		inTruth := map[string]int{}
		for _, i := range ids {
			inTruth[truth[i]]++
		}
		if len(inTruth) == 1 {
			for t := range inTruth {
				if truthSize[t] == len(ids) {
					correct += len(ids)
				}
			}
		}
		for _, n := range inTruth {
			if n > 1 {
				accuratePairs += comb2(n)
			}
		}
	}
	precision, recall := accuratePairs/parsedPairs, accuratePairs/realPairs
	return float64(correct) / float64(len(truth)), 2 * precision * recall / (precision + recall)
}

// TestLoghub parses the Loghub 2,000-line samples with the published settings and compares the
// grouping accuracy with the published results. It needs the samples: run tools/fetch-loghub.sh,
// then LOGHUB=build/loghub go test -run TestLoghub -v ./internal/drain
func TestLoghub(t *testing.T) {
	dir := os.Getenv("LOGHUB")
	if dir == "" {
		t.Skip("set LOGHUB to the folder tools/fetch-loghub.sh fills")
	}
	if !filepath.IsAbs(dir) {
		dir = filepath.Join("..", "..", dir) // a relative path is taken from the repository root
	}
	var sum, sumPub float64
	for _, d := range loghub {
		re, err := formatRegex(d.format)
		if err != nil {
			t.Fatalf("%s: %v", d.name, err)
		}
		content := re.SubexpIndex("Content")
		var masks []*regexp.Regexp
		for _, m := range d.masks {
			masks = append(masks, regexp.MustCompile(m))
		}
		f, err := os.Open(filepath.Join(dir, d.name, d.name+"_2k.log"))
		if err != nil {
			t.Fatal(err)
		}
		p := New(Options{Depth: d.depth, Similarity: d.st})
		var got []*Cluster
		skipped := 0
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 1<<20), 1<<20)
		for sc.Scan() {
			m := re.FindStringSubmatch(strings.TrimSpace(sc.Text()))
			if m == nil {
				skipped++
				continue
			}
			msg := m[content]
			for _, mk := range masks {
				msg = mk.ReplaceAllLiteralString(msg, Wildcard)
			}
			c, _ := p.Add(strings.Fields(msg))
			got = append(got, c)
		}
		f.Close()
		parsed := make([]string, len(got))
		for i, c := range got {
			parsed[i] = strings.Join(c.Template, " ")
		}
		cf, err := os.Open(filepath.Join(dir, d.name, d.name+"_2k.log_structured.csv"))
		if err != nil {
			t.Fatal(err)
		}
		recs, err := csv.NewReader(cf).ReadAll()
		cf.Close()
		if err != nil {
			t.Fatal(err)
		}
		col := -1
		for i, h := range recs[0] {
			if h == "EventId" {
				col = i
			}
		}
		var truth, keep []string
		for i, r := range recs[1:] {
			if r[col] == "" || i >= len(parsed) {
				continue
			}
			truth = append(truth, r[col])
			keep = append(keep, parsed[i])
		}
		acc, f1 := accuracy(truth, keep)
		sum += acc
		sumPub += d.accuracy
		mark := ""
		if math.Abs(acc-d.accuracy) > 0.00005 {
			mark = "  differs"
			t.Errorf("%s: accuracy %.4f, published %.4f", d.name, acc, d.accuracy)
		}
		t.Logf("%-12s %5d lines %4d templates  accuracy %.4f (published %.4f)  F1 %.6f%s%s", d.name, len(got), len(p.Clusters()), acc, d.accuracy, f1, mark,
			map[bool]string{true: fmt.Sprintf("  %d lines skipped", skipped), false: ""}[skipped > 0])
	}
	t.Logf("average accuracy %.4f (published %.4f)", sum/float64(len(loghub)), sumPub/float64(len(loghub)))
}
