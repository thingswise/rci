package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/op/go-logging"

	"k8s.io/kubernetes/pkg/util/jsonpath"
)

var log = logging.MustGetLogger("rci")

// Example format string. Everything except the message has a custom color
// which is dependent on the log level. Many fields have a custom output
// formatting too, eg. the time returns the hour down to the milli second.
var format = logging.MustStringFormatter(
	"%{time:15:04:05.000} %{shortfunc} > %{level:.4s} %{id:03x} %{message}",
)

var (
	debug  = flag.Bool("v", false, "verbose output")
	url    = flag.String("a", "", "target URL")
	method = flag.String("m", "GET", "HTTP method")
	body   = flag.String("b", "",
		"request body (for POST, PUT and other requests with a body);\n"+
			"if the value starts with @ then the rest is considered a name\n"+
			"of the file to read the body from; the special filename `-`\n"+
			"indicates standard input")
	error_map = flag.String("r", "",
		"response mapping; the format is `X1;X2;X3...` where Xi is\n"+
			"CODE=MAPPING; CODE is either a numeric HTTP response code or\n"+
			"a template `2XX`, `4XX`, `5XX`; MAPPING is either a number which\n"+
			"indicates a process exit code (EC) or `EC:MESSAGE_TEMPLATE` where\n"+
			"MESSAGE_TEMPLATE is a string with {}-enclosed jsonpath expressions;\n"+
			"the expressions follow the general syntax of Kubernetes jsonpath\n"+
			"(http://kubernetes.io/docs/user-guide/jsonpath/) with the response\n"+
			"JSON message being the root document")
)

var Usage = func() {
	fmt.Fprintf(os.Stderr, "Usage of %s:\n", os.Args[0])
	flag.PrintDefaults()
}

func main() {
	logging.SetFormatter(format)

	flag.Usage = Usage
	flag.Parse()

	if *debug {
		logging.SetLevel(logging.DEBUG, "rci")
	} else {
		logging.SetLevel(logging.ERROR, "rci")
	}

	err_map, err := parse_error_map(*error_map)
	if err != nil {
		fmt.Printf("Invalid error map: %s. Error: %s\n", *error_map, err)
		os.Exit(1)
	}

	if *url == "" {
		fmt.Printf("No url provided\n")
		os.Exit(1)
	}

	var buf io.Reader = nil

	if *method == "POST" || *method == "PUT" {
		if strings.HasPrefix(*body, "@") {
			filename := (*body)[1:len(*body)]
			text, err := ioutil.ReadFile(filename)
			if err != nil {
				fmt.Printf("Cannot read file %s: %s\n", filename, err)
				os.Exit(1)
			}
			buf = bytes.NewReader(text)
		} else {
			buf = strings.NewReader(*body)
		}
	}

	req, err := http.NewRequest(*method, *url, buf)
	if err != nil {
		fmt.Printf("Cannot create HTTP request: %s\n", err)
		os.Exit(1)
	}

	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		fmt.Printf("Cannot execute command request: %s\n", err)
		os.Exit(1)
	}

	status_code_str := fmt.Sprintf("%d", resp.StatusCode)
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		m1, ok := err_map[status_code_str]
		if ok {
			handle_resp(m1, resp, "")
		} else {
			m2, ok := err_map["2XX"]
			if ok {
				handle_resp(m2, resp, "")
			} else {
				// by default handle 2XX as OK
				// do nothing...
			}
		}
	} else if resp.StatusCode >= 400 && resp.StatusCode < 500 {
		m1, ok := err_map[status_code_str]
		if ok {
			handle_resp(m1, resp, resp.Status)
		} else {
			m2, ok := err_map["4XX"]
			if ok {
				handle_resp(m2, resp, resp.Status)
			} else {
				fmt.Errorf("Unexpected response: %d %s\n", resp.StatusCode, resp.Status)
				os.Exit(1)
			}
		}
	} else if resp.StatusCode >= 500 && resp.StatusCode < 600 {
		m1, ok := err_map[status_code_str]
		if ok {
			handle_resp(m1, resp, resp.Status)
		} else {
			m2, ok := err_map["5XX"]
			if ok {
				handle_resp(m2, resp, resp.Status)
			} else {
				fmt.Errorf("Unexpected response: %d %s\n", resp.StatusCode, resp.Status)
				os.Exit(1)
			}
		}
	} else {
		m1, ok := err_map[status_code_str]
		if ok {
			handle_resp(m1, resp, resp.Status)
		} else {
			fmt.Errorf("Unexpected response: %d %s\n", resp.StatusCode, resp.Status)
			os.Exit(1)
		}
	}
}

func handle_resp(m ErrorMapping, resp *http.Response, default_message string) {
	var message string = default_message

	if m.template != nil {
		if resp.Header.Get("content-type") == "application/json" {
			var data interface{}
			decoder := json.NewDecoder(resp.Body)
			if err := decoder.Decode(&data); err != nil {
				log.Fatalf("Cannot process JSON response: %s", err)
			}

			var b bytes.Buffer
			if err := m.template.Execute(&b, &data); err != nil {
				// simply use default_message
			} else {
				message = b.String()
			}
		}
	}

	fmt.Printf("%s\n", message)
	os.Exit(m.exit_code)
}

type ErrorMapping struct {
	exit_code int
	template  *jsonpath.JSONPath
}

func parse_error_map(err_map string) (map[string]ErrorMapping, error) {
	result := make(map[string]ErrorMapping)

	if err_map == "" {
		return result, nil
	}

	maps := strings.Split(err_map, ";")
	for m := range maps {
		codemapping := strings.SplitN(maps[m], "=", 2)
		if len(codemapping) == 2 {
			code := codemapping[0]
			ec_template_ := codemapping[1]
			if code == "2XX" {

			} else if code == "4XX" {

			} else if code == "5XX" {

			} else {
				if _, err := strconv.Atoi(code); err != nil {
					return nil, fmt.Errorf("Invalid HTTP code: %s", code)
				}
			}
			ec_template := strings.SplitN(ec_template_, ":", 2)
			if len(ec_template) == 1 {
				exit_code, err := strconv.Atoi(ec_template[0])
				if err != nil {
					return nil, fmt.Errorf("Invalid exit code: %s", ec_template[0])
				}
				result[code] = ErrorMapping{exit_code: exit_code, template: nil}
			} else if len(ec_template) == 2 {
				exit_code, err := strconv.Atoi(ec_template[0])
				if err != nil {
					return nil, fmt.Errorf("Invalid exit code: %s", ec_template[0])
				}
				template := jsonpath.New("path_" + code)

				if err := template.Parse(ec_template[1]); err != nil {
					return nil, fmt.Errorf("Cannot parse json path: %s. Error: %s", ec_template[1], err)
				}
				result[code] = ErrorMapping{exit_code: exit_code, template: template}
			} else {
				return nil, fmt.Errorf("Invalid error mapping: %s", codemapping)
			}
		} else {
			return nil, fmt.Errorf("Invalid error mapping: %s", codemapping)
		}
	}
	return result, nil
}
