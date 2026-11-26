package config

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// Plan is what onboarding proposes before anything is written. It is a fast,
// read-only preview; `muster setup` does the real work.
type Plan struct {
	Dir      string
	Name     string
	Stack    string // compose, k8s, docker, "" when unknown
	Found    []string
	Services []PlannedService
	// Fallback is true when the Go preview found nothing it understands; the
	// CLI's own scanner (`muster setup --scan`) is used instead.
	Fallback bool
}

// PlannedService is one detected service.
type PlannedService struct {
	Key     string
	Name    string
	Include bool
	Infra   bool
	Image   string
	Build   string
	Health  Health
	Creds   string
}

var infraPorts = map[string]int{
	"redis": 6379, "postgres": 5432, "postgresql": 5432, "mysql": 3306, "mariadb": 3306,
	"mongo": 27017, "mongodb": 27017, "rabbitmq": 5672, "memcached": 11211,
	"elasticsearch": 9200, "kafka": 9092, "zookeeper": 2181, "nats": 4222, "minio": 9000,
	"valkey": 6379, "clickhouse": 8123, "cassandra": 9042,
}

var composeNames = []string{"docker-compose.yml", "docker-compose.yaml", "compose.yml", "compose.yaml"}

// Scan inspects dir and proposes services, health checks and a deploy order.
func Scan(dir string) (Plan, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return Plan{}, err
	}
	plan := Plan{Dir: abs, Name: filepath.Base(abs)}
	for _, sub := range []string{"", "docker", "deploy", "infra"} {
		for _, n := range composeNames {
			p := filepath.Join(abs, sub, n)
			if _, err := os.Stat(p); err == nil {
				svcs, err := scanCompose(p)
				if err != nil {
					return plan, fmt.Errorf("reading %s: %w", rel(abs, p), err)
				}
				plan.Stack = "compose"
				plan.Found = append(plan.Found, rel(abs, p))
				plan.Services = svcs
				orderPlan(&plan)
				return plan, nil
			}
		}
	}
	for _, sub := range []string{"k8s", "kubernetes", "deploy", "manifests"} {
		d := filepath.Join(abs, sub)
		if st, err := os.Stat(d); err == nil && st.IsDir() {
			svcs := scanK8s(d)
			if len(svcs) > 0 {
				plan.Stack = "k8s"
				plan.Found = append(plan.Found, sub+"/")
				plan.Services = svcs
				orderPlan(&plan)
				return plan, nil
			}
		}
	}
	if p := filepath.Join(abs, "Dockerfile"); fileExists(p) {
		plan.Stack = "docker"
		plan.Found = append(plan.Found, "Dockerfile")
		svc := PlannedService{Key: "app", Name: "App", Include: true, Build: ".", Creds: "off"}
		if port := dockerfileExpose(p); port > 0 {
			svc.Health = Health{Enabled: true, Type: "http", Endpoint: "/", Port: port, Timeout: 10}
		}
		plan.Services = []PlannedService{svc}
		return plan, nil
	}
	for _, f := range []string{"package.json", "go.mod", "requirements.txt", "pyproject.toml", "Cargo.toml", "Gemfile", "pom.xml", "build.gradle"} {
		if fileExists(filepath.Join(abs, f)) {
			plan.Found = append(plan.Found, f)
		}
	}
	plan.Fallback = true
	return plan, nil
}

type composeFile struct {
	Services yaml.Node `yaml:"services"`
}

type composeService struct {
	Image       string    `yaml:"image"`
	Build       yaml.Node `yaml:"build"`
	Ports       []any     `yaml:"ports"`
	Expose      []any     `yaml:"expose"`
	Healthcheck *struct {
		Test any `yaml:"test"`
	} `yaml:"healthcheck"`
	Profiles []string `yaml:"profiles"`
}

func scanCompose(path string) ([]PlannedService, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cf composeFile
	if err := yaml.Unmarshal(data, &cf); err != nil {
		return nil, err
	}
	if cf.Services.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("no services: section")
	}
	var out []PlannedService
	for i := 0; i+1 < len(cf.Services.Content); i += 2 {
		name := cf.Services.Content[i].Value
		var cs composeService
		if err := cf.Services.Content[i+1].Decode(&cs); err != nil {
			continue
		}
		ps := PlannedService{Key: ToKey(name), Name: FriendlyName(name), Include: len(cs.Profiles) == 0, Image: cs.Image, Creds: "off"}
		switch cs.Build.Kind {
		case yaml.ScalarNode:
			ps.Build = cs.Build.Value
		case yaml.MappingNode:
			ps.Build = "."
			for j := 0; j+1 < len(cs.Build.Content); j += 2 {
				if cs.Build.Content[j].Value == "context" {
					ps.Build = cs.Build.Content[j+1].Value
				}
			}
		}
		host, container := firstPort(cs.Ports)
		if host == 0 {
			host = firstExpose(cs.Expose)
			container = host
		}
		infraName := infraKind(cs.Image, name)
		ps.Infra = infraName != "" && ps.Build == ""
		switch {
		case ps.Infra:
			port := host
			if port == 0 {
				port = infraPorts[infraName]
			}
			ps.Health = Health{Enabled: port > 0, Type: "tcp", Port: port, Timeout: 10}
		case host > 0:
			ps.Health = Health{Enabled: true, Type: "http", Endpoint: "/", Port: host, Timeout: 10}
		}
		_ = container
		out = append(out, ps)
	}
	return out, nil
}

func infraKind(image, name string) string {
	img := strings.ToLower(image)
	if i := strings.LastIndex(img, "/"); i >= 0 {
		img = img[i+1:]
	}
	if i := strings.IndexAny(img, ":@"); i >= 0 {
		img = img[:i]
	}
	for _, cand := range []string{img, strings.ToLower(name)} {
		if _, ok := infraPorts[cand]; ok {
			return cand
		}
		for k := range infraPorts {
			if cand != "" && strings.HasPrefix(cand, k) {
				return k
			}
		}
	}
	return ""
}

func firstPort(ports []any) (host, container int) {
	for _, p := range ports {
		switch v := p.(type) {
		case string:
			if h, c := parsePortSpec(v); h > 0 {
				return h, c
			}
		case int:
			return v, v
		case map[string]any:
			pub := toInt(v["published"])
			tgt := toInt(v["target"])
			if pub == 0 {
				pub = tgt
			}
			if pub > 0 {
				return pub, tgt
			}
		}
	}
	return 0, 0
}

// parsePortSpec handles "80", "3000:80", "127.0.0.1:8080:80", "[::1]:80:80",
// "8000-8010:8000-8010" and "/udp" suffixes.
func parsePortSpec(spec string) (host, container int) {
	spec = strings.TrimSpace(spec)
	if i := strings.Index(spec, "/"); i >= 0 {
		spec = spec[:i]
	}
	if strings.HasPrefix(spec, "[") {
		if i := strings.Index(spec, "]:"); i >= 0 {
			spec = spec[i+2:]
		}
	}
	parts := strings.Split(spec, ":")
	num := func(s string) int {
		n, _ := strconv.Atoi(strings.Split(s, "-")[0])
		return n
	}
	switch len(parts) {
	case 1:
		n := num(parts[0])
		return n, n
	case 2:
		return num(parts[0]), num(parts[1])
	default:
		return num(parts[len(parts)-2]), num(parts[len(parts)-1])
	}
}

func firstExpose(exp []any) int {
	for _, e := range exp {
		if n := toInt(e); n > 0 {
			return n
		}
	}
	return 0
}

func toInt(v any) int {
	switch t := v.(type) {
	case int:
		return t
	case string:
		n, _ := strconv.Atoi(strings.Split(t, "/")[0])
		return n
	}
	return 0
}

type k8sDoc struct {
	Kind     string `yaml:"kind"`
	Metadata struct {
		Name string `yaml:"name"`
	} `yaml:"metadata"`
	Spec struct {
		Template struct {
			Spec struct {
				Containers []struct {
					Image string `yaml:"image"`
					Ports []struct {
						ContainerPort int `yaml:"containerPort"`
					} `yaml:"ports"`
					ReadinessProbe *probe `yaml:"readinessProbe"`
					LivenessProbe  *probe `yaml:"livenessProbe"`
				} `yaml:"containers"`
			} `yaml:"spec"`
		} `yaml:"template"`
	} `yaml:"spec"`
}

type probe struct {
	HTTPGet *struct {
		Path string `yaml:"path"`
		Port any    `yaml:"port"`
	} `yaml:"httpGet"`
	TCPSocket *struct {
		Port any `yaml:"port"`
	} `yaml:"tcpSocket"`
}

func scanK8s(dir string) []PlannedService {
	var out []PlannedService
	seen := map[string]bool{}
	_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !(strings.HasSuffix(path, ".yaml") || strings.HasSuffix(path, ".yml")) {
			return nil
		}
		f, err := os.Open(path)
		if err != nil {
			return nil
		}
		defer f.Close()
		dec := yaml.NewDecoder(f)
		for {
			var doc k8sDoc
			if err := dec.Decode(&doc); err != nil {
				break
			}
			if doc.Kind != "Deployment" && doc.Kind != "StatefulSet" || doc.Metadata.Name == "" || seen[doc.Metadata.Name] {
				continue
			}
			seen[doc.Metadata.Name] = true
			ps := PlannedService{Key: ToKey(doc.Metadata.Name), Name: FriendlyName(doc.Metadata.Name), Include: true, Creds: "off"}
			if cs := doc.Spec.Template.Spec.Containers; len(cs) > 0 {
				c := cs[0]
				ps.Image = c.Image
				pr := c.ReadinessProbe
				if pr == nil {
					pr = c.LivenessProbe
				}
				switch {
				case pr != nil && pr.HTTPGet != nil:
					ps.Health = Health{Enabled: true, Type: "http", Endpoint: pr.HTTPGet.Path, Port: toInt(pr.HTTPGet.Port), Timeout: 10}
				case pr != nil && pr.TCPSocket != nil:
					ps.Health = Health{Enabled: true, Type: "tcp", Port: toInt(pr.TCPSocket.Port), Timeout: 10}
				case len(c.Ports) > 0:
					ps.Health = Health{Enabled: true, Type: "tcp", Port: c.Ports[0].ContainerPort, Timeout: 10}
				}
				if ps.Health.Port == 0 {
					ps.Health.Enabled = false
				}
				ps.Infra = infraKind(c.Image, doc.Metadata.Name) != ""
			}
			out = append(out, ps)
		}
		return nil
	})
	return out
}

func dockerfileExpose(path string) int {
	f, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) >= 2 && strings.EqualFold(fields[0], "EXPOSE") {
			return toInt(fields[1])
		}
	}
	return 0
}

// orderPlan puts infrastructure first, then built services, then the rest,
// keeping file order within each group.
func orderPlan(p *Plan) {
	rank := func(s PlannedService) int {
		switch {
		case s.Infra:
			return 0
		case s.Build != "":
			return 1
		}
		return 2
	}
	sort.SliceStable(p.Services, func(i, j int) bool { return rank(p.Services[i]) < rank(p.Services[j]) })
}

// HealthSpec renders the --health flag value for muster setup.
func (s PlannedService) HealthSpec() string {
	h := s.Health
	if !h.Enabled {
		return s.Key + "=none"
	}
	switch h.Type {
	case "http":
		ep := h.Endpoint
		if ep == "" {
			ep = "/"
		}
		return fmt.Sprintf("%s=http:%s:%d", s.Key, ep, h.Port)
	case "tcp":
		return fmt.Sprintf("%s=tcp:%d", s.Key, h.Port)
	case "command":
		return fmt.Sprintf("%s=command:%s", s.Key, h.Command)
	}
	return s.Key + "=none"
}

// SetupArgs builds the non-interactive `muster setup` invocation for the plan.
func (p Plan) SetupArgs() []string {
	args := []string{"setup", "--path", p.Dir}
	if p.Fallback || len(p.Services) == 0 {
		return append(args, "--scan")
	}
	var keys []string
	for _, s := range p.Services {
		if s.Include {
			keys = append(keys, s.Key)
		}
	}
	args = append(args, "--stack", p.Stack, "--services", strings.Join(keys, ","), "--order", strings.Join(keys, ","), "--name", p.Name)
	for _, s := range p.Services {
		if !s.Include {
			continue
		}
		args = append(args, "--health", s.HealthSpec())
		if s.Creds != "" && s.Creds != "off" {
			args = append(args, "--creds", s.Key+"="+s.Creds)
		}
	}
	return args
}

// Included returns the services that will be created.
func (p Plan) Included() []PlannedService {
	var out []PlannedService
	for _, s := range p.Services {
		if s.Include {
			out = append(out, s)
		}
	}
	return out
}

// ToKey mirrors the CLI's _svc_to_key.
func ToKey(name string) string {
	var b strings.Builder
	lastUnderscore := false
	for _, r := range strings.ToLower(name) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
			lastUnderscore = false
		} else if !lastUnderscore {
			b.WriteByte('_')
			lastUnderscore = true
		}
	}
	return strings.Trim(b.String(), "_")
}

// FriendlyName turns "my-api" into "My Api" (API/DB style acronyms kept).
func FriendlyName(name string) string {
	parts := strings.FieldsFunc(name, func(r rune) bool { return r == '-' || r == '_' || r == ' ' || r == '.' })
	for i, p := range parts {
		switch strings.ToLower(p) {
		case "api", "db", "ui", "ws", "cdn", "dns", "sql":
			parts[i] = strings.ToUpper(p)
		default:
			if p != "" {
				parts[i] = strings.ToUpper(p[:1]) + p[1:]
			}
		}
	}
	return strings.Join(parts, " ")
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

func rel(base, p string) string {
	if r, err := filepath.Rel(base, p); err == nil {
		return r
	}
	return p
}
