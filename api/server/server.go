package server

import (
	"bufio"
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"expvar"
	"fmt"
	"io"
	"io/ioutil"
	"net"
	"net/http"
	"net/http/pprof"
	"os"
	"strconv"
	"strings"
	"syscall"

	"code.google.com/p/go.net/websocket"
	"github.com/docker/libcontainer/user"
	"github.com/gorilla/mux"

	"github.com/docker/docker/api"
	"github.com/docker/docker/engine"
	"github.com/docker/docker/pkg/listenbuffer"
	"github.com/docker/docker/pkg/log"
	"github.com/docker/docker/pkg/parsers"
	"github.com/docker/docker/pkg/systemd"
	"github.com/docker/docker/pkg/version"
	"github.com/docker/docker/registry"
	"github.com/docker/docker/utils"
)

var (
	// 两个job之间打通，一个是acceptconnections，另一个是serverapi
	// serverapi 初始化完成后，才能acceptconnections
	activationLock chan struct{}
)

type HttpApiFunc func(eng *engine.Engine, version version.Version, w http.ResponseWriter, r *http.Request, vars map[string]string) error

func hijackServer(w http.ResponseWriter) (io.ReadCloser, io.Writer, error) {
	conn, _, err := w.(http.Hijacker).Hijack()
	if err != nil {
		return nil, nil, err
	}
	// Flush the options to make sure the client sets the raw mode
	conn.Write([]byte{})
	return conn, conn, nil
}

//If we don't do this, POST method without Content-type (even with empty body) will fail
func parseForm(r *http.Request) error {
	if r == nil {
		return nil
	}
	if err := r.ParseForm(); err != nil && !strings.HasPrefix(err.Error(), "mime:") {
		return err
	}
	return nil
}

func parseMultipartForm(r *http.Request) error {
	if err := r.ParseMultipartForm(4096); err != nil && !strings.HasPrefix(err.Error(), "mime:") {
		return err
	}
	return nil
}

func httpError(w http.ResponseWriter, err error) {
	statusCode := http.StatusInternalServerError
	// FIXME: this is brittle and should not be necessary.
	// If we need to differentiate between different possible error types, we should
	// create appropriate error types with clearly defined meaning.
	if strings.Contains(err.Error(), "No such") {
		statusCode = http.StatusNotFound
	} else if strings.Contains(err.Error(), "Bad parameter") {
		statusCode = http.StatusBadRequest
	} else if strings.Contains(err.Error(), "Conflict") {
		statusCode = http.StatusConflict
	} else if strings.Contains(err.Error(), "Impossible") {
		statusCode = http.StatusNotAcceptable
	} else if strings.Contains(err.Error(), "Wrong login/password") {
		statusCode = http.StatusUnauthorized
	} else if strings.Contains(err.Error(), "hasn't been activated") {
		statusCode = http.StatusForbidden
	}

	if err != nil {
		log.Errorf("HTTP Error: statusCode=%d %s", statusCode, err.Error())
		http.Error(w, err.Error(), statusCode)
	}
}

func writeJSON(w http.ResponseWriter, code int, v engine.Env) error {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	return v.Encode(w)
}

func streamJSON(job *engine.Job, w http.ResponseWriter, flush bool) {
	w.Header().Set("Content-Type", "application/json")
	if flush {
		job.Stdout.Add(utils.NewWriteFlusher(w))
	} else {
		job.Stdout.Add(w)
	}
}

func getBoolParam(value string) (bool, error) {
	if value == "" {
		return false, nil
	}
	ret, err := strconv.ParseBool(value)
	if err != nil {
		return false, fmt.Errorf("Bad parameter")
	}
	return ret, nil
}

func postAuth(eng *engine.Engine, version version.Version, w http.ResponseWriter, r *http.Request, vars map[string]string) error {
	var (
		authConfig, err = ioutil.ReadAll(r.Body)
		job             = eng.Job("auth")
		stdoutBuffer    = bytes.NewBuffer(nil)
	)
	if err != nil {
		return err
	}
	job.Setenv("authConfig", string(authConfig))
	job.Stdout.Add(stdoutBuffer)
	if err = job.Run(); err != nil {
		return err
	}
	if status := engine.Tail(stdoutBuffer, 1); status != "" {
		var env engine.Env
		env.Set("Status", status)
		return writeJSON(w, http.StatusOK, env)
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func getVersion(eng *engine.Engine, version version.Version, w http.ResponseWriter, r *http.Request, vars map[string]string) error {
	w.Header().Set("Content-Type", "application/json")
	eng.ServeHTTP(w, r)
	return nil
}

func postContainersKill(eng *engine.Engine, version version.Version, w http.ResponseWriter, r *http.Request, vars map[string]string) error {
	if vars == nil {
		return fmt.Errorf("Missing parameter")
	}
	if err := parseForm(r); err != nil {
		return err
	}
	job := eng.Job("kill", vars["name"])
	if sig := r.Form.Get("signal"); sig != "" {
		job.Args = append(job.Args, sig)
	}
	if err := job.Run(); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func postContainersPause(eng *engine.Engine, version version.Version, w http.ResponseWriter, r *http.Request, vars map[string]string) error {
	if vars == nil {
		return fmt.Errorf("Missing parameter")
	}
	if err := parseForm(r); err != nil {
		return err
	}
	job := eng.Job("pause", vars["name"])
	if err := job.Run(); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func postContainersUnpause(eng *engine.Engine, version version.Version, w http.ResponseWriter, r *http.Request, vars map[string]string) error {
	if vars == nil {
		return fmt.Errorf("Missing parameter")
	}
	if err := parseForm(r); err != nil {
		return err
	}
	job := eng.Job("unpause", vars["name"])
	if err := job.Run(); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func getContainersExport(eng *engine.Engine, version version.Version, w http.ResponseWriter, r *http.Request, vars map[string]string) error {
	if vars == nil {
		return fmt.Errorf("Missing parameter")
	}
	job := eng.Job("export", vars["name"])
	job.Stdout.Add(w)
	if err := job.Run(); err != nil {
		return err
	}
	return nil
}

func getImagesJSON(eng *engine.Engine, version version.Version, w http.ResponseWriter, r *http.Request, vars map[string]string) error {
	if err := parseForm(r); err != nil {
		return err
	}

	var (
		err  error
		outs *engine.Table
		job  = eng.Job("images")
	)

	job.Setenv("filters", r.Form.Get("filters"))
	// FIXME this parameter could just be a match filter
	job.Setenv("filter", r.Form.Get("filter"))
	job.Setenv("all", r.Form.Get("all"))

	if version.GreaterThanOrEqualTo("1.7") {
		streamJSON(job, w, false)
	} else if outs, err = job.Stdout.AddListTable(); err != nil {
		return err
	}

	if err := job.Run(); err != nil {
		return err
	}

	if version.LessThan("1.7") && outs != nil { // Convert to legacy format
		outsLegacy := engine.NewTable("Created", 0)
		for _, out := range outs.Data {
			for _, repoTag := range out.GetList("RepoTags") {
				repo, tag := parsers.ParseRepositoryTag(repoTag)
				outLegacy := &engine.Env{}
				outLegacy.Set("Repository", repo)
				outLegacy.SetJson("Tag", tag)
				outLegacy.Set("Id", out.Get("Id"))
				outLegacy.SetInt64("Created", out.GetInt64("Created"))
				outLegacy.SetInt64("Size", out.GetInt64("Size"))
				outLegacy.SetInt64("VirtualSize", out.GetInt64("VirtualSize"))
				outsLegacy.Add(outLegacy)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		if _, err := outsLegacy.WriteListTo(w); err != nil {
			return err
		}
	}
	return nil
}

func getImagesViz(eng *engine.Engine, version version.Version, w http.ResponseWriter, r *http.Request, vars map[string]string) error {
	if version.GreaterThan("1.6") {
		w.WriteHeader(http.StatusNotFound)
		return fmt.Errorf("This is now implemented in the client.")
	}
	eng.ServeHTTP(w, r)
	return nil
}

func getInfo(eng *engine.Engine, version version.Version, w http.ResponseWriter, r *http.Request, vars map[string]string) error {
	w.Header().Set("Content-Type", "application/json")
	eng.ServeHTTP(w, r)
	return nil
}

func getEvents(eng *engine.Engine, version version.Version, w http.ResponseWriter, r *http.Request, vars map[string]string) error {
	if err := parseForm(r); err != nil {
		return err
	}

	var job = eng.Job("events")
	streamJSON(job, w, true)
	job.Setenv("since", r.Form.Get("since"))
	job.Setenv("until", r.Form.Get("until"))
	return job.Run()
}

func getImagesHistory(eng *engine.Engine, version version.Version, w http.ResponseWriter, r *http.Request, vars map[string]string) error {
	if vars == nil {
		return fmt.Errorf("Missing parameter")
	}

	var job = eng.Job("history", vars["name"])
	streamJSON(job, w, false)

	if err := job.Run(); err != nil {
		return err
	}
	return nil
}

func getContainersChanges(eng *engine.Engine, version version.Version, w http.ResponseWriter, r *http.Request, vars map[string]string) error {
	if vars == nil {
		return fmt.Errorf("Missing parameter")
	}
	var job = eng.Job("container_changes", vars["name"])
	streamJSON(job, w, false)

	return job.Run()
}

func getContainersTop(eng *engine.Engine, version version.Version, w http.ResponseWriter, r *http.Request, vars map[string]string) error {
	if version.LessThan("1.4") {
		return fmt.Errorf("top was improved a lot since 1.3, Please upgrade your docker client.")
	}
	if vars == nil {
		return fmt.Errorf("Missing parameter")
	}
	if err := parseForm(r); err != nil {
		return err
	}

	job := eng.Job("top", vars["name"], r.Form.Get("ps_args"))
	streamJSON(job, w, false)
	return job.Run()
}

func getContainersJSON(eng *engine.Engine, version version.Version, w http.ResponseWriter, r *http.Request, vars map[string]string) error {
	if err := parseForm(r); err != nil {
		return err
	}
	var (
		err  error
		outs *engine.Table
		job  = eng.Job("containers")
	)

	job.Setenv("all", r.Form.Get("all"))
	job.Setenv("size", r.Form.Get("size"))
	job.Setenv("since", r.Form.Get("since"))
	job.Setenv("before", r.Form.Get("before"))
	job.Setenv("limit", r.Form.Get("limit"))
	job.Setenv("filters", r.Form.Get("filters"))

	if version.GreaterThanOrEqualTo("1.5") {
		streamJSON(job, w, false)
	} else if outs, err = job.Stdout.AddTable(); err != nil {
		return err
	}
	if err = job.Run(); err != nil {
		return err
	}
	if version.LessThan("1.5") { // Convert to legacy format
		for _, out := range outs.Data {
			ports := engine.NewTable("", 0)
			ports.ReadListFrom([]byte(out.Get("Ports")))
			out.Set("Ports", api.DisplayablePorts(ports))
		}
		w.Header().Set("Content-Type", "application/json")
		if _, err = outs.WriteListTo(w); err != nil {
			return err
		}
	}
	return nil
}

func getContainersLogs(eng *engine.Engine, version version.Version, w http.ResponseWriter, r *http.Request, vars map[string]string) error {
	if err := parseForm(r); err != nil {
		return err
	}
	if vars == nil {
		return fmt.Errorf("Missing parameter")
	}

	var (
		inspectJob = eng.Job("container_inspect", vars["name"])
		logsJob    = eng.Job("logs", vars["name"])
		c, err     = inspectJob.Stdout.AddEnv()
	)
	if err != nil {
		return err
	}
	logsJob.Setenv("follow", r.Form.Get("follow"))
	logsJob.Setenv("tail", r.Form.Get("tail"))
	logsJob.Setenv("stdout", r.Form.Get("stdout"))
	logsJob.Setenv("stderr", r.Form.Get("stderr"))
	logsJob.Setenv("timestamps", r.Form.Get("timestamps"))
	// Validate args here, because we can't return not StatusOK after job.Run() call
	stdout, stderr := logsJob.GetenvBool("stdout"), logsJob.GetenvBool("stderr")
	if !(stdout || stderr) {
		return fmt.Errorf("Bad parameters: you must choose at least one stream")
	}
	if err = inspectJob.Run(); err != nil {
		return err
	}

	var outStream, errStream io.Writer
	outStream = utils.NewWriteFlusher(w)

	if c.GetSubEnv("Config") != nil && !c.GetSubEnv("Config").GetBool("Tty") && version.GreaterThanOrEqualTo("1.6") {
		errStream = utils.NewStdWriter(outStream, utils.Stderr)
		outStream = utils.NewStdWriter(outStream, utils.Stdout)
	} else {
		errStream = outStream
	}

	logsJob.Stdout.Add(outStream)
	logsJob.Stderr.Set(errStream)
	if err := logsJob.Run(); err != nil {
		fmt.Fprintf(outStream, "Error running logs job: %s\n", err)
	}
	return nil
}

func postImagesTag(eng *engine.Engine, version version.Version, w http.ResponseWriter, r *http.Request, vars map[string]string) error {
	if err := parseForm(r); err != nil {
		return err
	}
	if vars == nil {
		return fmt.Errorf("Missing parameter")
	}

	job := eng.Job("tag", vars["name"], r.Form.Get("repo"), r.Form.Get("tag"))
	job.Setenv("force", r.Form.Get("force"))
	if err := job.Run(); err != nil {
		return err
	}
	w.WriteHeader(http.StatusCreated)
	return nil
}

func postCommit(eng *engine.Engine, version version.Version, w http.ResponseWriter, r *http.Request, vars map[string]string) error {
	if err := parseForm(r); err != nil {
		return err
	}
	var (
		config       engine.Env
		env          engine.Env
		job          = eng.Job("commit", r.Form.Get("container"))
		stdoutBuffer = bytes.NewBuffer(nil)
	)
	if err := config.Decode(r.Body); err != nil {
		log.Errorf("%s", err)
	}

	if r.FormValue("pause") == "" && version.GreaterThanOrEqualTo("1.13") {
		job.Setenv("pause", "1")
	} else {
		job.Setenv("pause", r.FormValue("pause"))
	}

	job.Setenv("repo", r.Form.Get("repo"))
	job.Setenv("tag", r.Form.Get("tag"))
	job.Setenv("author", r.Form.Get("author"))
	job.Setenv("comment", r.Form.Get("comment"))
	job.SetenvSubEnv("config", &config)

	job.Stdout.Add(stdoutBuffer)
	if err := job.Run(); err != nil {
		return err
	}
	env.Set("Id", engine.Tail(stdoutBuffer, 1))
	return writeJSON(w, http.StatusCreated, env)
}

// Creates an image from Pull or from Import
func postImagesCreate(eng *engine.Engine, version version.Version, w http.ResponseWriter, r *http.Request, vars map[string]string) error {
	if err := parseForm(r); err != nil {
		return err
	}

	var (
		image = r.Form.Get("fromImage")
		repo  = r.Form.Get("repo")
		tag   = r.Form.Get("tag")
		job   *engine.Job
	)
	authEncoded := r.Header.Get("X-Registry-Auth")
	authConfig := &registry.AuthConfig{}
	if authEncoded != "" {
		authJson := base64.NewDecoder(base64.URLEncoding, strings.NewReader(authEncoded))
		if err := json.NewDecoder(authJson).Decode(authConfig); err != nil {
			// for a pull it is not an error if no auth was given
			// to increase compatibility with the existing api it is defaulting to be empty
			authConfig = &registry.AuthConfig{}
		}
	}
	if image != "" { //pull
		if tag == "" {
			image, tag = parsers.ParseRepositoryTag(image)
		}
		metaHeaders := map[string][]string{}
		for k, v := range r.Header {
			if strings.HasPrefix(k, "X-Meta-") {
				metaHeaders[k] = v
			}
		}
		job = eng.Job("pull", image, tag)
		job.SetenvBool("parallel", version.GreaterThan("1.3"))
		job.SetenvJson("metaHeaders", metaHeaders)
		job.SetenvJson("authConfig", authConfig)
	} else { //import
		if tag == "" {
			repo, tag = parsers.ParseRepositoryTag(repo)
		}
		job = eng.Job("import", r.Form.Get("fromSrc"), repo, tag)
		job.Stdin.Add(r.Body)
	}

	if version.GreaterThan("1.0") {
		job.SetenvBool("json", true)
		streamJSON(job, w, true)
	} else {
		job.Stdout.Add(utils.NewWriteFlusher(w))
	}
	if err := job.Run(); err != nil {
		if !job.Stdout.Used() {
			return err
		}
		sf := utils.NewStreamFormatter(version.GreaterThan("1.0"))
		w.Write(sf.FormatError(err))
	}

	return nil
}

func getImagesSearch(eng *engine.Engine, version version.Version, w http.ResponseWriter, r *http.Request, vars map[string]string) error {
	if err := parseForm(r); err != nil {
		return err
	}
	var (
		authEncoded = r.Header.Get("X-Registry-Auth")
		authConfig  = &registry.AuthConfig{}
		metaHeaders = map[string][]string{}
	)

	if authEncoded != "" {
		authJson := base64.NewDecoder(base64.URLEncoding, strings.NewReader(authEncoded))
		if err := json.NewDecoder(authJson).Decode(authConfig); err != nil {
			// for a search it is not an error if no auth was given
			// to increase compatibility with the existing api it is defaulting to be empty
			authConfig = &registry.AuthConfig{}
		}
	}
	for k, v := range r.Header {
		if strings.HasPrefix(k, "X-Meta-") {
			metaHeaders[k] = v
		}
	}

	var job = eng.Job("search", r.Form.Get("term"))
	job.SetenvJson("metaHeaders", metaHeaders)
	job.SetenvJson("authConfig", authConfig)
	streamJSON(job, w, false)

	return job.Run()
}

func postImagesPush(eng *engine.Engine, version version.Version, w http.ResponseWriter, r *http.Request, vars map[string]string) error {
	if vars == nil {
		return fmt.Errorf("Missing parameter")
	}

	metaHeaders := map[string][]string{}
	for k, v := range r.Header {
		if strings.HasPrefix(k, "X-Meta-") {
			metaHeaders[k] = v
		}
	}
	if err := parseForm(r); err != nil {
		return err
	}
	authConfig := &registry.AuthConfig{}

	authEncoded := r.Header.Get("X-Registry-Auth")
	if authEncoded != "" {
		// the new format is to handle the authConfig as a header
		authJson := base64.NewDecoder(base64.URLEncoding, strings.NewReader(authEncoded))
		if err := json.NewDecoder(authJson).Decode(authConfig); err != nil {
			// to increase compatibility to existing api it is defaulting to be empty
			authConfig = &registry.AuthConfig{}
		}
	} else {
		// the old format is supported for compatibility if there was no authConfig header
		if err := json.NewDecoder(r.Body).Decode(authConfig); err != nil {
			return err
		}
	}

	job := eng.Job("push", vars["name"])
	job.SetenvJson("metaHeaders", metaHeaders)
	job.SetenvJson("authConfig", authConfig)
	job.Setenv("tag", r.Form.Get("tag"))
	if version.GreaterThan("1.0") {
		job.SetenvBool("json", true)
		streamJSON(job, w, true)
	} else {
		job.Stdout.Add(utils.NewWriteFlusher(w))
	}

	if err := job.Run(); err != nil {
		if !job.Stdout.Used() {
			return err
		}
		sf := utils.NewStreamFormatter(version.GreaterThan("1.0"))
		w.Write(sf.FormatError(err))
	}
	return nil
}

func getImagesGet(eng *engine.Engine, version version.Version, w http.ResponseWriter, r *http.Request, vars map[string]string) error {
	if vars == nil {
		return fmt.Errorf("Missing parameter")
	}
	if version.GreaterThan("1.0") {
		w.Header().Set("Content-Type", "application/x-tar")
	}
	job := eng.Job("image_export", vars["name"])
	job.Stdout.Add(w)
	return job.Run()
}

func postImagesLoad(eng *engine.Engine, version version.Version, w http.ResponseWriter, r *http.Request, vars map[string]string) error {
	job := eng.Job("load")
	job.Stdin.Add(r.Body)
	return job.Run()
}

func postContainersCreate(eng *engine.Engine, version version.Version, w http.ResponseWriter, r *http.Request, vars map[string]string) error {
	if err := parseForm(r); err != nil {
		return nil
	}
	var (
		out          engine.Env
		job          = eng.Job("create", r.Form.Get("name"))
		outWarnings  []string
		stdoutBuffer = bytes.NewBuffer(nil)
		warnings     = bytes.NewBuffer(nil)
	)
	if err := job.DecodeEnv(r.Body); err != nil {
		return err
	}
	// Read container ID from the first line of stdout
	job.Stdout.Add(stdoutBuffer)
	// Read warnings from stderr
	job.Stderr.Add(warnings)
	if err := job.Run(); err != nil {
		return err
	}
	// Parse warnings from stderr
	scanner := bufio.NewScanner(warnings)
	for scanner.Scan() {
		outWarnings = append(outWarnings, scanner.Text())
	}
	out.Set("Id", engine.Tail(stdoutBuffer, 1))
	out.SetList("Warnings", outWarnings)
	return writeJSON(w, http.StatusCreated, out)
}

func postContainersRestart(eng *engine.Engine, version version.Version, w http.ResponseWriter, r *http.Request, vars map[string]string) error {
	if err := parseForm(r); err != nil {
		return err
	}
	if vars == nil {
		return fmt.Errorf("Missing parameter")
	}
	job := eng.Job("restart", vars["name"])
	job.Setenv("t", r.Form.Get("t"))
	if err := job.Run(); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func deleteContainers(eng *engine.Engine, version version.Version, w http.ResponseWriter, r *http.Request, vars map[string]string) error {
	if err := parseForm(r); err != nil {
		return err
	}
	if vars == nil {
		return fmt.Errorf("Missing parameter")
	}
	job := eng.Job("delete", vars["name"])

	job.Setenv("forceRemove", r.Form.Get("force"))

	job.Setenv("removeVolume", r.Form.Get("v"))
	job.Setenv("removeLink", r.Form.Get("link"))
	if err := job.Run(); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func deleteImages(eng *engine.Engine, version version.Version, w http.ResponseWriter, r *http.Request, vars map[string]string) error {
	if err := parseForm(r); err != nil {
		return err
	}
	if vars == nil {
		return fmt.Errorf("Missing parameter")
	}
	var job = eng.Job("image_delete", vars["name"])
	streamJSON(job, w, false)
	job.Setenv("force", r.Form.Get("force"))
	job.Setenv("noprune", r.Form.Get("noprune"))

	return job.Run()
}

func postContainersStart(eng *engine.Engine, version version.Version, w http.ResponseWriter, r *http.Request, vars map[string]string) error {
	if vars == nil {
		return fmt.Errorf("Missing parameter")
	}
	var (
		name = vars["name"]
		job  = eng.Job("start", name)
	)

	// allow a nil body for backwards compatibility
	if r.Body != nil && r.ContentLength > 0 {
		if !api.MatchesContentType(r.Header.Get("Content-Type"), "application/json") {
			return fmt.Errorf("Content-Type of application/json is required")
		}

		if err := job.DecodeEnv(r.Body); err != nil {
			return err
		}
	}

	if err := job.Run(); err != nil {
		if err.Error() == "Container already started" {
			w.WriteHeader(http.StatusNotModified)
			return nil
		}
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func postContainersStop(eng *engine.Engine, version version.Version, w http.ResponseWriter, r *http.Request, vars map[string]string) error {
	if err := parseForm(r); err != nil {
		return err
	}
	if vars == nil {
		return fmt.Errorf("Missing parameter")
	}
	job := eng.Job("stop", vars["name"])
	job.Setenv("t", r.Form.Get("t"))
	if err := job.Run(); err != nil {
		if err.Error() == "Container already stopped" {
			w.WriteHeader(http.StatusNotModified)
			return nil
		}
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func postContainersWait(eng *engine.Engine, version version.Version, w http.ResponseWriter, r *http.Request, vars map[string]string) error {
	if vars == nil {
		return fmt.Errorf("Missing parameter")
	}
	var (
		env          engine.Env
		stdoutBuffer = bytes.NewBuffer(nil)
		job          = eng.Job("wait", vars["name"])
	)
	job.Stdout.Add(stdoutBuffer)
	if err := job.Run(); err != nil {
		return err
	}

	env.Set("StatusCode", engine.Tail(stdoutBuffer, 1))
	return writeJSON(w, http.StatusOK, env)
}

func postContainersResize(eng *engine.Engine, version version.Version, w http.ResponseWriter, r *http.Request, vars map[string]string) error {
	if err := parseForm(r); err != nil {
		return err
	}
	if vars == nil {
		return fmt.Errorf("Missing parameter")
	}
	if err := eng.Job("resize", vars["name"], r.Form.Get("h"), r.Form.Get("w")).Run(); err != nil {
		return err
	}
	return nil
}

func postContainersAttach(eng *engine.Engine, version version.Version, w http.ResponseWriter, r *http.Request, vars map[string]string) error {
	if err := parseForm(r); err != nil {
		return err
	}
	if vars == nil {
		return fmt.Errorf("Missing parameter")
	}

	var (
		job    = eng.Job("container_inspect", vars["name"])
		c, err = job.Stdout.AddEnv()
	)
	if err != nil {
		return err
	}
	if err = job.Run(); err != nil {
		return err
	}

	inStream, outStream, err := hijackServer(w)
	if err != nil {
		return err
	}
	defer func() {
		if tcpc, ok := inStream.(*net.TCPConn); ok {
			tcpc.CloseWrite()
		} else {
			inStream.Close()
		}
	}()
	defer func() {
		if tcpc, ok := outStream.(*net.TCPConn); ok {
			tcpc.CloseWrite()
		} else if closer, ok := outStream.(io.Closer); ok {
			closer.Close()
		}
	}()

	var errStream io.Writer

	fmt.Fprintf(outStream, "HTTP/1.1 200 OK\r\nContent-Type: application/vnd.docker.raw-stream\r\n\r\n")

	if c.GetSubEnv("Config") != nil && !c.GetSubEnv("Config").GetBool("Tty") && version.GreaterThanOrEqualTo("1.6") {
		errStream = utils.NewStdWriter(outStream, utils.Stderr)
		outStream = utils.NewStdWriter(outStream, utils.Stdout)
	} else {
		errStream = outStream
	}

	job = eng.Job("attach", vars["name"])
	job.Setenv("logs", r.Form.Get("logs"))
	job.Setenv("stream", r.Form.Get("stream"))
	job.Setenv("stdin", r.Form.Get("stdin"))
	job.Setenv("stdout", r.Form.Get("stdout"))
	job.Setenv("stderr", r.Form.Get("stderr"))
	job.Stdin.Add(inStream)
	job.Stdout.Add(outStream)
	job.Stderr.Set(errStream)
	if err := job.Run(); err != nil {
		fmt.Fprintf(outStream, "Error attaching: %s\n", err)

	}
	return nil
}

func wsContainersAttach(eng *engine.Engine, version version.Version, w http.ResponseWriter, r *http.Request, vars map[string]string) error {
	if err := parseForm(r); err != nil {
		return err
	}
	if vars == nil {
		return fmt.Errorf("Missing parameter")
	}

	if err := eng.Job("container_inspect", vars["name"]).Run(); err != nil {
		return err
	}

	h := websocket.Handler(func(ws *websocket.Conn) {
		defer ws.Close()
		job := eng.Job("attach", vars["name"])
		job.Setenv("logs", r.Form.Get("logs"))
		job.Setenv("stream", r.Form.Get("stream"))
		job.Setenv("stdin", r.Form.Get("stdin"))
		job.Setenv("stdout", r.Form.Get("stdout"))
		job.Setenv("stderr", r.Form.Get("stderr"))
		job.Stdin.Add(ws)
		job.Stdout.Add(ws)
		job.Stderr.Set(ws)
		if err := job.Run(); err != nil {
			log.Errorf("Error attaching websocket: %s", err)
		}
	})
	h.ServeHTTP(w, r)

	return nil
}

func getContainersByName(eng *engine.Engine, version version.Version, w http.ResponseWriter, r *http.Request, vars map[string]string) error {
	if vars == nil {
		return fmt.Errorf("Missing parameter")
	}
	var job = eng.Job("container_inspect", vars["name"])
	if version.LessThan("1.12") {
		job.SetenvBool("raw", true)
	}
	streamJSON(job, w, false)
	return job.Run()
}

func getImagesByName(eng *engine.Engine, version version.Version, w http.ResponseWriter, r *http.Request, vars map[string]string) error {
	if vars == nil {
		return fmt.Errorf("Missing parameter")
	}
	var job = eng.Job("image_inspect", vars["name"])
	if version.LessThan("1.12") {
		job.SetenvBool("raw", true)
	}
	streamJSON(job, w, false)
	return job.Run()
}

func postBuild(eng *engine.Engine, version version.Version, w http.ResponseWriter, r *http.Request, vars map[string]string) error {
	if version.LessThan("1.3") {
		return fmt.Errorf("Multipart upload for build is no longer supported. Please upgrade your docker client.")
	}
	var (
		authEncoded       = r.Header.Get("X-Registry-Auth")
		authConfig        = &registry.AuthConfig{}
		configFileEncoded = r.Header.Get("X-Registry-Config")
		configFile        = &registry.ConfigFile{}
		job               = eng.Job("build")
	)

	// This block can be removed when API versions prior to 1.9 are deprecated.
	// Both headers will be parsed and sent along to the daemon, but if a non-empty
	// ConfigFile is present, any value provided as an AuthConfig directly will
	// be overridden. See BuildFile::CmdFrom for details.
	if version.LessThan("1.9") && authEncoded != "" {
		authJson := base64.NewDecoder(base64.URLEncoding, strings.NewReader(authEncoded))
		if err := json.NewDecoder(authJson).Decode(authConfig); err != nil {
			// for a pull it is not an error if no auth was given
			// to increase compatibility with the existing api it is defaulting to be empty
			authConfig = &registry.AuthConfig{}
		}
	}

	if configFileEncoded != "" {
		configFileJson := base64.NewDecoder(base64.URLEncoding, strings.NewReader(configFileEncoded))
		if err := json.NewDecoder(configFileJson).Decode(configFile); err != nil {
			// for a pull it is not an error if no auth was given
			// to increase compatibility with the existing api it is defaulting to be empty
			configFile = &registry.ConfigFile{}
		}
	}

	if version.GreaterThanOrEqualTo("1.8") {
		job.SetenvBool("json", true)
		streamJSON(job, w, true)
	} else {
		job.Stdout.Add(utils.NewWriteFlusher(w))
	}

	if r.FormValue("forcerm") == "1" && version.GreaterThanOrEqualTo("1.12") {
		job.Setenv("rm", "1")
	} else if r.FormValue("rm") == "" && version.GreaterThanOrEqualTo("1.12") {
		job.Setenv("rm", "1")
	} else {
		job.Setenv("rm", r.FormValue("rm"))
	}
	job.Stdin.Add(r.Body)
	job.Setenv("remote", r.FormValue("remote"))
	job.Setenv("t", r.FormValue("t"))
	job.Setenv("q", r.FormValue("q"))
	job.Setenv("nocache", r.FormValue("nocache"))
	job.Setenv("forcerm", r.FormValue("forcerm"))
	job.SetenvJson("authConfig", authConfig)
	job.SetenvJson("configFile", configFile)

	if err := job.Run(); err != nil {
		if !job.Stdout.Used() {
			return err
		}
		sf := utils.NewStreamFormatter(version.GreaterThanOrEqualTo("1.8"))
		w.Write(sf.FormatError(err))
	}
	return nil
}

func postContainersCopy(eng *engine.Engine, version version.Version, w http.ResponseWriter, r *http.Request, vars map[string]string) error {
	if vars == nil {
		return fmt.Errorf("Missing parameter")
	}

	var copyData engine.Env

	if contentType := r.Header.Get("Content-Type"); api.MatchesContentType(contentType, "application/json") {
		if err := copyData.Decode(r.Body); err != nil {
			return err
		}
	} else {
		return fmt.Errorf("Content-Type not supported: %s", contentType)
	}

	if copyData.Get("Resource") == "" {
		return fmt.Errorf("Path cannot be empty")
	}

	origResource := copyData.Get("Resource")

	if copyData.Get("Resource")[0] == '/' {
		copyData.Set("Resource", copyData.Get("Resource")[1:])
	}

	job := eng.Job("container_copy", vars["name"], copyData.Get("Resource"))
	job.Stdout.Add(w)
	if err := job.Run(); err != nil {
		log.Errorf("%s", err.Error())
		if strings.Contains(err.Error(), "No such container") {
			w.WriteHeader(http.StatusNotFound)
		} else if strings.Contains(err.Error(), "no such file or directory") {
			return fmt.Errorf("Could not find the file %s in container %s", origResource, vars["name"])
		}
	}
	return nil
}

func optionsHandler(eng *engine.Engine, version version.Version, w http.ResponseWriter, r *http.Request, vars map[string]string) error {
	w.WriteHeader(http.StatusOK)
	return nil
}
func writeCorsHeaders(w http.ResponseWriter, r *http.Request) {
	w.Header().Add("Access-Control-Allow-Origin", "*")
	w.Header().Add("Access-Control-Allow-Headers", "Origin, X-Requested-With, Content-Type, Accept")
	w.Header().Add("Access-Control-Allow-Methods", "GET, POST, DELETE, PUT, OPTIONS")
}

func ping(eng *engine.Engine, version version.Version, w http.ResponseWriter, r *http.Request, vars map[string]string) error {
	_, err := w.Write([]byte{'O', 'K'})
	return err
}

func makeHttpHandler(eng *engine.Engine, logging bool, localMethod string, localRoute string, handlerFunc HttpApiFunc, enableCors bool, dockerVersion version.Version) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// log the request
		log.Debugf("Calling %s %s", localMethod, localRoute)

		if logging {
			log.Infof("%s %s", r.Method, r.RequestURI)
		}

		if strings.Contains(r.Header.Get("User-Agent"), "Docker-Client/") {
			userAgent := strings.Split(r.Header.Get("User-Agent"), "/")
			if len(userAgent) == 2 && !dockerVersion.Equal(version.Version(userAgent[1])) {
				log.Debugf("Warning: client and server don't have the same version (client: %s, server: %s)", userAgent[1], dockerVersion)
			}
		}
		version := version.Version(mux.Vars(r)["version"])
		if version == "" {
			version = api.APIVERSION
		}
		if enableCors {
			writeCorsHeaders(w, r)
		}

		if version.GreaterThan(api.APIVERSION) {
			http.Error(w, fmt.Errorf("client and server don't have same version (client : %s, server: %s)", version, api.APIVERSION).Error(), http.StatusNotFound)
			return
		}

		if err := handlerFunc(eng, version, w, r, mux.Vars(r)); err != nil {
			log.Errorf("Handler for %s %s returned error: %s", localMethod, localRoute, err)
			httpError(w, err)
		}
	}
}

// Replicated from expvar.go as not public.
func expvarHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	fmt.Fprintf(w, "{\n")
	first := true
	expvar.Do(func(kv expvar.KeyValue) {
		if !first {
			fmt.Fprintf(w, ",\n")
		}
		first = false
		fmt.Fprintf(w, "%q: %s", kv.Key, kv.Value)
	})
	fmt.Fprintf(w, "\n}\n")
}

func AttachProfiler(router *mux.Router) {
	router.HandleFunc("/debug/vars", expvarHandler)
	router.HandleFunc("/debug/pprof/", pprof.Index)
	router.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	router.HandleFunc("/debug/pprof/profile", pprof.Profile)
	router.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	router.HandleFunc("/debug/pprof/heap", pprof.Handler("heap").ServeHTTP)
	router.HandleFunc("/debug/pprof/goroutine", pprof.Handler("goroutine").ServeHTTP)
	router.HandleFunc("/debug/pprof/threadcreate", pprof.Handler("threadcreate").ServeHTTP)
}

func createRouter(eng *engine.Engine, logging, enableCors bool, dockerVersion string) (*mux.Router, error) {
	r := mux.NewRouter()
	if os.Getenv("DEBUG") != "" {
		AttachProfiler(r)
	}
	m := map[string]map[string]HttpApiFunc{
		"GET": {
			"/_ping":                          ping,
			"/events":                         getEvents,
			"/info":                           getInfo,
			"/version":                        getVersion,
			"/images/json":                    getImagesJSON,
			"/images/viz":                     getImagesViz,
			"/images/search":                  getImagesSearch,
			"/images/{name:.*}/get":           getImagesGet,
			"/images/{name:.*}/history":       getImagesHistory,
			"/images/{name:.*}/json":          getImagesByName,
			"/containers/ps":                  getContainersJSON,
			"/containers/json":                getContainersJSON,
			"/containers/{name:.*}/export":    getContainersExport,
			"/containers/{name:.*}/changes":   getContainersChanges,
			"/containers/{name:.*}/json":      getContainersByName,
			"/containers/{name:.*}/top":       getContainersTop,
			"/containers/{name:.*}/logs":      getContainersLogs,
			"/containers/{name:.*}/attach/ws": wsContainersAttach,
		},
		"POST": {
			"/auth":                         postAuth,
			"/commit":                       postCommit,
			"/build":                        postBuild,
			"/images/create":                postImagesCreate,
			"/images/load":                  postImagesLoad,
			"/images/{name:.*}/push":        postImagesPush,
			"/images/{name:.*}/tag":         postImagesTag,
			"/containers/create":            postContainersCreate,
			"/containers/{name:.*}/kill":    postContainersKill,
			"/containers/{name:.*}/pause":   postContainersPause,
			"/containers/{name:.*}/unpause": postContainersUnpause,
			"/containers/{name:.*}/restart": postContainersRestart,
			"/containers/{name:.*}/start":   postContainersStart,
			"/containers/{name:.*}/stop":    postContainersStop,
			"/containers/{name:.*}/wait":    postContainersWait,
			"/containers/{name:.*}/resize":  postContainersResize,
			"/containers/{name:.*}/attach":  postContainersAttach,
			"/containers/{name:.*}/copy":    postContainersCopy,
		},
		"DELETE": {
			"/containers/{name:.*}": deleteContainers,
			"/images/{name:.*}":     deleteImages,
		},
		"OPTIONS": {
			"": optionsHandler,
		},
	}

	for method, routes := range m {
		for route, fct := range routes {
			log.Debugf("Registering %s, %s", method, route)
			// NOTE: scope issue, make sure the variables are local and won't be changed
			localRoute := route
			localFct := fct
			localMethod := method

			// build the handler function
			f := makeHttpHandler(eng, logging, localMethod, localRoute, localFct, enableCors, version.Version(dockerVersion))

			// add the new route
			if localRoute == "" {
				r.Methods(localMethod).HandlerFunc(f)
			} else {
				r.Path("/v{version:[0-9.]+}" + localRoute).Methods(localMethod).HandlerFunc(f)
				r.Path(localRoute).Methods(localMethod).HandlerFunc(f)
			}
		}
	}

	return r, nil
}

// ServeRequest processes a single http request to the docker remote api.
// FIXME: refactor this to be part of Server and not require re-creating a new
// router each time. This requires first moving ListenAndServe into Server.
func ServeRequest(eng *engine.Engine, apiversion version.Version, w http.ResponseWriter, req *http.Request) error {
	router, err := createRouter(eng, false, true, "")
	if err != nil {
		return err
	}
	// Insert APIVERSION into the request as a convenience
	req.URL.Path = fmt.Sprintf("/v%s%s", apiversion, req.URL.Path)
	router.ServeHTTP(w, req)
	return nil
}

// ServeFD creates an http.Server and sets it up to serve given a socket activated
// argument.
func ServeFd(addr string, handle http.Handler) error {
	ls, e := systemd.ListenFD(addr)
	if e != nil {
		return e
	}

	chErrors := make(chan error, len(ls))

	// We don't want to start serving on these sockets until the
	// daemon is initialized and installed. Otherwise required handlers
	// won't be ready.
	<-activationLock

	// Since ListenFD will return one or more sockets we have
	// to create a go func to spawn off multiple serves
	for i := range ls {
		listener := ls[i]
		go func() {
			httpSrv := http.Server{Handler: handle}
			chErrors <- httpSrv.Serve(listener)
		}()
	}

	for i := 0; i < len(ls); i += 1 {
		err := <-chErrors
		if err != nil {
			return err
		}
	}

	return nil
}

func lookupGidByName(nameOrGid string) (int, error) {
	groups, err := user.ParseGroupFilter(func(g *user.Group) bool {
		return g.Name == nameOrGid || strconv.Itoa(g.Gid) == nameOrGid
	})
	if err != nil {
		return -1, err
	}
	if groups != nil && len(groups) > 0 {
		return groups[0].Gid, nil
	}
	return -1, fmt.Errorf("Group %s not found", nameOrGid)
}

func changeGroup(addr string, nameOrGid string) error {
	gid, err := lookupGidByName(nameOrGid)
	if err != nil {
		return err
	}

	log.Debugf("%s group found. gid: %d", nameOrGid, gid)
	return os.Chown(addr, 0, gid)
}

// ListenAndServe sets up the required http.Server and gets it listening for
// each addr passed in and does protocol specific checking.
func ListenAndServe(proto, addr string, job *engine.Job) error {
	var l net.Listener
	r, err := createRouter(job.Eng, job.GetenvBool("Logging"), job.GetenvBool("EnableCors"), job.Getenv("Version"))
	if err != nil {
		return err
	}

	if proto == "fd" {
		return ServeFd(addr, r)
	}

	if proto == "unix" {
		if err := syscall.Unlink(addr); err != nil && !os.IsNotExist(err) {
			return err
		}
	}

	var oldmask int
	if proto == "unix" {
		oldmask = syscall.Umask(0777)
	}

	if job.GetenvBool("BufferRequests") {
		l, err = listenbuffer.NewListenBuffer(proto, addr, activationLock)
	} else {
		l, err = net.Listen(proto, addr)
	}

	if proto == "unix" {
		syscall.Umask(oldmask)
	}
	if err != nil {
		return err
	}

	if proto != "unix" && (job.GetenvBool("Tls") || job.GetenvBool("TlsVerify")) {
		tlsCert := job.Getenv("TlsCert")
		tlsKey := job.Getenv("TlsKey")
		cert, err := tls.LoadX509KeyPair(tlsCert, tlsKey)
		if err != nil {
			return fmt.Errorf("Couldn't load X509 key pair (%s, %s): %s. Key encrypted?",
				tlsCert, tlsKey, err)
		}
		tlsConfig := &tls.Config{
			NextProtos:   []string{"http/1.1"},
			Certificates: []tls.Certificate{cert},
		}
		if job.GetenvBool("TlsVerify") {
			certPool := x509.NewCertPool()
			file, err := ioutil.ReadFile(job.Getenv("TlsCa"))
			if err != nil {
				return fmt.Errorf("Couldn't read CA certificate: %s", err)
			}
			certPool.AppendCertsFromPEM(file)

			tlsConfig.ClientAuth = tls.RequireAndVerifyClientCert
			tlsConfig.ClientCAs = certPool
		}
		l = tls.NewListener(l, tlsConfig)
	}

	// Basic error and sanity checking
	switch proto {
	case "tcp":
		if !strings.HasPrefix(addr, "127.0.0.1") && !job.GetenvBool("TlsVerify") {
			log.Infof("/!\\ DON'T BIND ON ANOTHER IP ADDRESS THAN 127.0.0.1 IF YOU DON'T KNOW WHAT YOU'RE DOING /!\\")
		}
	case "unix":
		socketGroup := job.Getenv("SocketGroup")
		if socketGroup != "" {
			if err := changeGroup(addr, socketGroup); err != nil {
				if socketGroup == "docker" {
					// if the user hasn't explicitly specified the group ownership, don't fail on errors.
					log.Debugf("Warning: could not chgrp %s to docker: %s", addr, err.Error())
				} else {
					return err
				}
			}
		}
		if err := os.Chmod(addr, 0660); err != nil {
			return err
		}
	default:
		return fmt.Errorf("Invalid protocol format.")
	}

	httpSrv := http.Server{Addr: addr, Handler: r}
	return httpSrv.Serve(l)
}

// ServeApi loops through all of the protocols sent in to docker and spawns
// off a go routine to setup a serving http.Server for each.
func ServeApi(job *engine.Job) engine.Status {
	if len(job.Args) == 0 {
		return job.Errorf("usage: %s PROTO://ADDR [PROTO://ADDR ...]", job.Name)
	}
	var (
		protoAddrs = job.Args
		chErrors   = make(chan error, len(protoAddrs))
	)
	activationLock = make(chan struct{})

	for _, protoAddr := range protoAddrs {
		protoAddrParts := strings.SplitN(protoAddr, "://", 2)
		if len(protoAddrParts) != 2 {
			return job.Errorf("usage: %s PROTO://ADDR [PROTO://ADDR ...]", job.Name)
		}
		go func() {
			log.Infof("Listening for HTTP on %s (%s)", protoAddrParts[0], protoAddrParts[1])
			chErrors <- ListenAndServe(protoAddrParts[0], protoAddrParts[1], job)
		}()
	}

	for i := 0; i < len(protoAddrs); i += 1 {
		err := <-chErrors
		if err != nil {
			return job.Error(err)
		}
	}

	return engine.StatusOK
}

// activationLock是一个用来同步”serveapi”和”acceptconnections”这两个 job 执行的 channel。
// 在 serveapi 运行时 ServeFd 和 ListenAndServe 的实现中，由于 activationLock 这个 channel 中没有内容而阻塞，
// 而当运行”acceptionconnections”这个 job 时，会首先通知 init 进程 Docker Daemon 已经启动完毕，并关闭 activationLock，
// 同时也开启了 serveapi 的继续执行。正是由于 activationLock 的存在，
// 保证了”acceptconnections”这个job 的运行起到通知”serveapi”开启正式服务于 API 的效果
func AcceptConnections(job *engine.Job) engine.Status {
	// Tell the init daemon we are accepting requests
	go systemd.SdNotify("READY=1")

	// close the lock so the listeners start accepting connections
	if activationLock != nil {
		close(activationLock)
	}

	return engine.StatusOK
}
