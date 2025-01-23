package ring

import (
	"html/template"
	"net/http"
	"sort"
	"strings"
	"time"
)

const partitionPageContent = `
<!DOCTYPE html>
<html>
	<head>
		<meta charset="UTF-8">
		<title>Partition Ring Status</title>
	</head>
	<body>
		<h1>Partition Ring Status</h1>
		<form action="" method="POST">
			<input type="hidden" name="csrf_token" value="$__CSRF_TOKEN_PLACEHOLDER__">
			<table width="100%" border="1">
				<thead>
					<tr>
						<th>Partition ID</th>
						<th>State</th>
						<th>Timestamp</th>
						<th>Tokens</th>
						<th>Zones</th>
						<th>Instances</th>
					</tr>
				</thead>
				<tbody>
					{{ range $i, $p := .Partitions }}
					{{ if mod $i 2 }}
					<tr>
					{{ else }}
					<tr bgcolor="#BEBEBE">
					{{ end }}
						<td>{{ .ID }}</td>
						<td>{{ .State }}</td>
						<td>{{ .Timestamp }}</td>
						<td>{{ .NumTokens }}</td>
						<td>{{ .Zones }}</td>
						<td>{{ .Instances }}</td>
					</tr>
					{{ end }}
				</tbody>
			</table>
			<br>
		</form>
	</body>
</html>`

var partitionHttpPageTemplate *template.Template

func init() {
	t := template.New("webpage")
	t.Funcs(template.FuncMap{"mod": func(i, j int) bool { return i%j == 0 }})
	partitionHttpPageTemplate = template.Must(t.Parse(partitionPageContent))
}

type partitionHttpResponse struct {
	Partitions []partitionDesc `json:"partitions"`
}
type partitionDesc struct {
	ID        string `json:"id"`
	State     string `json:"state"`
	Instances string `json:"instances"`
	Zones     string `json:"zones"`
	Timestamp string `json:"timestamp"`
	NumTokens int    `json:"-"`
}

func (r *PartitionRing) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	r.mtx.RLock()
	defer r.mtx.RUnlock()

	partitionIds := []string{}
	if r.desc == nil || r.desc.Partitions == nil {
		w.Write([]byte("notfound"))
		w.WriteHeader(404)
		return
	}
	for id := range r.desc.Partitions {
		partitionIds = append(partitionIds, id)
	}
	sort.Strings(partitionIds)

	partitions := []partitionDesc{}

	for _, id := range partitionIds {
		p := r.desc.Partitions[id]

		instances := []string{}
		zones := []string{}
		for id, d := range p.Instances {
			instances = append(instances, id)
			zones = append(zones, d.Zone)
		}
		sort.Strings(instances)
		sort.Strings(zones)
		partitions = append(partitions, partitionDesc{
			ID:        id,
			State:     p.State.String(),
			Instances: strings.Join(instances, ","),
			Zones:     strings.Join(zones, ","),
			Timestamp: time.Unix(p.Timestamp, 0).String(),
			NumTokens: len(p.Tokens),
		})
	}

	renderPartitionHTTPResponse(w, partitionHttpResponse{
		Partitions: partitions,
	}, partitionHttpPageTemplate, req)
}

func renderPartitionHTTPResponse(w http.ResponseWriter, v partitionHttpResponse, t *template.Template, r *http.Request) {
	err := t.Execute(w, v)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}
