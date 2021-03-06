package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io/ioutil"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx"
)

type runsetPostResponse struct {
	RunSetID      int32
	RunIDs        []int32
	PullRequestID *int32 `json:",omitempty"`
}

type healthGetResponse struct {
	DatabaseResponds bool
}

type requestError struct {
	Explanation string
	httpStatus  int
}

func (rerr *requestError) Error() string {
	return rerr.Explanation
}

func (rerr *requestError) httpError(w http.ResponseWriter) {
	errorString := "{\"Explanation\": \"unknown\"}"
	bytes, err := json.Marshal(rerr)
	if err == nil {
		errorString = string(bytes)
	}
	http.Error(w, errorString, rerr.httpStatus)
}

func badRequestError(explanation string) *requestError {
	return &requestError{Explanation: explanation, httpStatus: http.StatusBadRequest}
}

func internalServerError(explanation string) *requestError {
	return &requestError{Explanation: explanation, httpStatus: http.StatusInternalServerError}
}

func runSetPutHandler(database *pgx.Tx, w http.ResponseWriter, r *http.Request, body []byte) (bool, *requestError) {
	var params RunSet
	if err := json.Unmarshal(body, &params); err != nil {
		fmt.Printf("Unmarshal error: %s\n", err.Error())
		return false, badRequestError("Could not parse request body")
	}

	reqErr := ensureMachineExists(database, params.Machine)
	if reqErr != nil {
		return false, reqErr
	}

	mainCommit, reqErr := ensureProductExists(database, params.MainProduct)
	if reqErr != nil {
		return false, reqErr
	}

	var secondaryCommits []string
	for _, p := range params.SecondaryProducts {
		commit, reqErr := ensureProductExists(database, p)
		if reqErr != nil {
			return false, reqErr
		}
		secondaryCommits = append(secondaryCommits, commit)
	}

	reqErr = params.ensureBenchmarksAndMetricsExist(database)
	if reqErr != nil {
		return false, reqErr
	}

	reqErr = ensureConfigExists(database, params.Config)
	if reqErr != nil {
		return false, reqErr
	}

	var pullRequestID *int32
	if params.PullRequest != nil {
		var prID int32
		pullRequestID = &prID
		err := database.QueryRow("insertPullRequest",
			params.PullRequest.BaselineRunSetID,
			params.PullRequest.URL).Scan(pullRequestID)
		if err != nil {
			fmt.Printf("pull request insert error: %s\n", err)
			return false, internalServerError("Could not insert pull request")
		}
	}

	var runSetID int32
	err := database.QueryRow("insertRunSet",
		params.StartedAt, params.FinishedAt,
		params.BuildURL, params.LogURLs,
		mainCommit, secondaryCommits, params.Machine.Name, params.Config.Name,
		params.TimedOutBenchmarks, params.CrashedBenchmarks,
		pullRequestID).Scan(&runSetID)
	if err != nil {
		fmt.Printf("run set insert error: %s\n", err)
		return false, internalServerError("Could not insert run set")
	}

	runIDs, reqErr := insertRuns(database, runSetID, params.Runs)
	if reqErr != nil {
		return false, reqErr
	}

	resp := runsetPostResponse{RunSetID: runSetID, RunIDs: runIDs, PullRequestID: pullRequestID}
	respBytes, err := json.Marshal(&resp)
	if err != nil {
		return false, internalServerError("Could not produce JSON for response")
	}

	w.WriteHeader(http.StatusCreated)
	w.Header().Set("Content-Type", "application/json; charset=UTF-8")
	w.Write(respBytes)

	return true, nil
}

func parseIDFromPath(path string, numComponents int, index int) (int32, *requestError) {
	pathComponents := strings.Split(path, "/")
	if len(pathComponents) != numComponents {
		return -1, badRequestError("Incorrect path")
	}
	id64, err := strconv.ParseInt(pathComponents[index], 10, 32)
	if err != nil {
		return -1, badRequestError("Could not parse run set id")
	}
	if id64 < 0 {
		return -1, badRequestError("Run set id must be a positive number")
	}
	return int32(id64), nil
}

func specificRunSetGetHandler(database *pgx.Tx, w http.ResponseWriter, r *http.Request, body []byte) (bool, *requestError) {
	runSetID, reqErr := parseIDFromPath(r.URL.Path, 4, 3)
	if reqErr != nil {
		return false, reqErr
	}

	rs, reqErr := fetchRunSet(database, runSetID, true)
	if reqErr != nil {
		return false, reqErr
	}

	respBytes, err := json.Marshal(&rs)
	if err != nil {
		return false, internalServerError("Could not product JSON for response")
	}

	w.WriteHeader(http.StatusOK)
	w.Header().Set("Content-Type", "application/json; charset=UTF-8")
	w.Write(respBytes)

	return false, nil
}

func specificRunSetDeleteHandler(database *pgx.Tx, w http.ResponseWriter, r *http.Request, body []byte) (bool, *requestError) {
	runSetID, reqErr := parseIDFromPath(r.URL.Path, 4, 3)
	if reqErr != nil {
		return false, reqErr
	}
	numRuns, numMetrics, err := deleteRunSet(database, runSetID)
	if err != nil {
		return false, err
	}

	status := make(map[string]int64)
	status["DeletedRunMetrics"] = numMetrics
	status["DeletedRuns"] = numRuns
	respBytes, err2 := json.Marshal(&status)
	if err2 != nil {
		return false, internalServerError("Could not product JSON for response")
	}

	w.WriteHeader(http.StatusOK)
	w.Header().Set("Content-Type", "application/json; charset=UTF-8")
	w.Write(respBytes)

	return true, nil
}

func specificRunSetPostHandler(database *pgx.Tx, w http.ResponseWriter, r *http.Request, body []byte) (bool, *requestError) {
	runSetID, reqErr := parseIDFromPath(r.URL.Path, 4, 3)
	if reqErr != nil {
		return false, reqErr
	}

	var params RunSet
	if err := json.Unmarshal(body, &params); err != nil {
		fmt.Printf("Unmarshal error: %s\n", err.Error())
		return false, badRequestError("Could not parse request body")
	}

	if params.PullRequest != nil {
		return false, badRequestError("PullRequest is not allowed for amending")
	}

	reqErr = params.ensureBenchmarksAndMetricsExist(database)
	if reqErr != nil {
		return false, reqErr
	}

	rs, reqErr := fetchRunSet(database, runSetID, false)
	if reqErr != nil {
		return false, reqErr
	}

	if !params.MainProduct.isSameAs(&rs.MainProduct) ||
		!productSetsEqual(params.SecondaryProducts, rs.SecondaryProducts) ||
		params.Machine != rs.Machine ||
		!params.Config.isSameAs(&rs.Config) {
		return false, badRequestError("Parameters do not match database")
	}

	rs.amendWithDataFrom(&params)

	runIDs, reqErr := insertRuns(database, runSetID, params.Runs)
	if reqErr != nil {
		return false, reqErr
	}

	reqErr = updateRunSet(database, runSetID, rs)
	if reqErr != nil {
		return false, reqErr
	}

	resp := runsetPostResponse{RunSetID: runSetID, RunIDs: runIDs}
	respBytes, err := json.Marshal(&resp)
	if err != nil {
		return false, internalServerError("Could not produce JSON for response")
	}

	w.WriteHeader(http.StatusCreated)
	w.Header().Set("Content-Type", "application/json; charset=UTF-8")
	w.Write([]byte(respBytes))

	return true, nil
}

func specificRunPostHandler(database *pgx.Tx, w http.ResponseWriter, r *http.Request, body []byte) (bool, *requestError) {
	runID, reqErr := parseIDFromPath(r.URL.Path, 4, 3)
	if reqErr != nil {
		return false, reqErr
	}

	var params Run
	if err := json.Unmarshal(body, &params); err != nil {
		fmt.Printf("Unmarshal error: %s\n", err.Error())
		return false, badRequestError("Could not parse request body")
	}

	reqErr = params.ensureBenchmarksAndMetricsExist(database, nil)
	if reqErr != nil {
		return false, reqErr
	}

	reqErr = insertResults(database, runID, params.Results, true)
	if reqErr != nil {
		return false, reqErr
	}

	w.WriteHeader(http.StatusCreated)
	w.Header().Set("Content-Type", "application/json; charset=UTF-8")
	w.Write([]byte("{}"))

	return true, nil
}

func runSetsGetHandler(database *pgx.Tx, w http.ResponseWriter, r *http.Request, body []byte) (bool, *requestError) {
	machine := r.URL.Query().Get("machine")
	config := r.URL.Query().Get("config")
	if machine == "" || config == "" {
		return false, badRequestError("Missing machine or config")
	}
	summaries, reqErr := fetchRunSetSummaries(database, machine, config)
	if reqErr != nil {
		return false, reqErr
	}
	if summaries == nil {
		summaries = []RunSetSummary{}
	}

	respBytes, err := json.Marshal(summaries)
	if err != nil {
		return false, internalServerError("Could not produce JSON for response")
	}

	w.WriteHeader(http.StatusOK)
	w.Header().Set("Content-Type", "application/json; charset=UTF-8")
	w.Write([]byte(respBytes))

	return false, nil
}

func healthHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		reqErr := &requestError{Explanation: "Method not allowed", httpStatus: http.StatusMethodNotAllowed}
		reqErr.httpError(w)
		return
	}

	var response healthGetResponse

	database, err := connPool.Begin()
	if err != nil {
		response.DatabaseResponds = false
	} else {
		_, reqErr := fetchBenchmarks(database)
		database.Rollback()

		response.DatabaseResponds = reqErr == nil
	}

	respBytes, err := json.Marshal(response)
	if err != nil {
		internalServerError("Could not produce JSON for response").httpError(w)
		return
	}

	if response.DatabaseResponds {
		w.WriteHeader(http.StatusOK)
	} else {
		w.WriteHeader(http.StatusServiceUnavailable)
	}
	w.Header().Set("Content-Type", "application/json; charset=UTF-8")
	w.Write([]byte(respBytes))
}

type handlerFunc func(database *pgx.Tx, w http.ResponseWriter, r *http.Request, body []byte) (bool, *requestError)

func isAuthorized(r *http.Request, authToken string) bool {
	if r.URL.Query().Get("authToken") == authToken {
		return true
	}
	if r.Header.Get("Authorization") == "token "+authToken {
		return true
	}
	return false
}

func newTransactionHandler(authToken string, handlers map[string]handlerFunc) func(w http.ResponseWriter, r *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		var reqErr *requestError
		handler, ok := handlers[r.Method]
		if !ok {
			reqErr = &requestError{Explanation: "Method not allowed", httpStatus: http.StatusMethodNotAllowed}
		} else if !isAuthorized(r, authToken) {
			reqErr = &requestError{Explanation: "Auth token invalid", httpStatus: http.StatusUnauthorized}
		} else {
			body, err := ioutil.ReadAll(r.Body)
			r.Body.Close()
			if err != nil {
				reqErr = internalServerError("Could not read request body: ")
			} else {
				transaction, err := connPool.Begin()
				if err != nil {
					reqErr = internalServerError("Could not begin transaction")
				} else {
					var commit bool
					commit, reqErr = handler(transaction, w, r, body)
					if commit {
						transaction.Commit()
					} else {
						transaction.Rollback()
					}
				}
			}
		}
		if reqErr != nil {
			reqErr.httpError(w)
		}
	}
}

func notFoundHandler(w http.ResponseWriter, r *http.Request) {
	reqErr := &requestError{Explanation: "No such endpoint", httpStatus: http.StatusNotFound}
	reqErr.httpError(w)
}

func main() {
	portFlag := flag.Int("port", 0, "port on which to listen")
	credentialsFlag := flag.String("credentials", "benchmarkerCredentials", "path of the credentials file")
	keyFlag := flag.String("ssl-key", "", "path of the SSL key file")
	certFlag := flag.String("ssl-certificate", "", "path of the SSL certificate file")
	flag.Parse()

	ssl := *certFlag != "" || *keyFlag != ""
	port := *portFlag
	if port == 0 {
		if ssl {
			port = 10443
		} else {
			port = 8081
		}
	}

	if err := readCredentials(*credentialsFlag); err != nil {
		fmt.Fprintf(os.Stderr, "Error: Cannot read credentials from file %s: %s\n", *credentialsFlag, err.Error())
		os.Exit(1)
	}

	initGitHub()

	if err := initDatabase(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: Cannot init DB: %s\n", err.Error())
		os.Exit(1)
	}

	authToken, err := getCredentialString("httpAPITokens", "default")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: Cannot get auth token: %s\n", err.Error())
		os.Exit(1)
	}

	http.HandleFunc("/api/runset", newTransactionHandler(authToken, map[string]handlerFunc{"PUT": runSetPutHandler}))
	http.HandleFunc("/api/runset/", newTransactionHandler(authToken, map[string]handlerFunc{"GET": specificRunSetGetHandler, "POST": specificRunSetPostHandler, "DELETE": specificRunSetDeleteHandler}))
	http.HandleFunc("/api/run/", newTransactionHandler(authToken, map[string]handlerFunc{"POST": specificRunPostHandler}))
	http.HandleFunc("/api/runsets", newTransactionHandler(authToken, map[string]handlerFunc{"GET": runSetsGetHandler}))
	http.HandleFunc("/api/health", healthHandler)
	http.HandleFunc("/", notFoundHandler)

	addr := fmt.Sprintf(":%d", port)
	fmt.Printf("listening at %s\n", addr)

	srv := &http.Server{
		Addr: addr,
		ReadTimeout: 360 * time.Second,
		WriteTimeout: 720 * time.Second,
	}

	if ssl {
		// Instructions for generating a certificate: http://www.zytrax.com/tech/survival/ssl.html#self
		err = srv.ListenAndServeTLS(*certFlag, *keyFlag)
	} else {
		err = srv.ListenAndServe()
	}

	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: Listen failed: %s\n", err.Error())
		os.Exit(1)
	}
}
