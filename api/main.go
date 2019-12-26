package main

import (
	"bytes"
	"encoding/json"
	"github.com/bugsnag/bugsnag-go"
	"github.com/gorilla/mux"
	"github.com/joho/godotenv"
	"github.com/pusher/pusher-http-go"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"
)

const API_VERSION = "v1"
const JSON_CONTENT_TYPE = "application/json"
const DASHBOARD_DATA_QUERY = "{\"query\": \"query {viewer{login id bio avatarUrl url name gists(first:50,privacy:ALL,orderBy:{field:CREATED_AT,direction:DESC}){edges{node{id files{name} description url updatedAt name isPublic}}}}}\"}"
const EVENT_GIST_DELETED = "gist-deleted"
const EVENT_ALL_GISTS_DELETED = "all-gists-deleted"

type accessTokenResponse struct {
	AccessToken      string `json:"access_token"`
	ErrorDescription string `json:"error_description"`
	Error            string `json:"error"`
}

type DashboardRequest struct {
	Code string `json:"code"`
	AccessToken string `json:"access_token"`
}

type DeleteRequest struct {
	AccessToken string `json:"access_token"`
	Id          string `json:"id"`
	Gists       []struct {
		ID       string `json:"id"`
		FileName string `json:"file_name"`
	} `json:"gists"`
}

type DashboardData struct {
	Data struct {
		Viewer struct {
			Login     string `json:"login"`
			Bio       string `json:"bio"`
			Id        string `json:"id"`
			AvatarURL string `json:"avatarUrl"`
			URL       string `json:"url"`
			Name      string `json:"name"`
			Gists     struct {
				Edges []struct {
					Node struct {
						ID    string `json:"id"`
						Files []struct {
							Name string `json:"name"`
						} `json:"files"`
						Description string    `json:"description"`
						URL         string    `json:"url"`
						UpdatedAt   time.Time `json:"updatedAt"`
						Name        string    `json:"name"`
						IsPublic    bool    `json:"isPublic"`
					} `json:"node"`
				} `json:"edges"`
			} `json:"gists"`
		} `json:"viewer"`
	} `json:"data"`
}

type HashMap map[string]interface{}

func handleCatchAll(responseWriter http.ResponseWriter, request *http.Request) {
	responseObject := map[string]interface{}{
		"apiVersion": API_VERSION,
		"url":        request.URL.String(),
	}

	writeResponseObject(responseWriter, responseObject)
}

func handleDelete(responseWriter http.ResponseWriter, request *http.Request) {
	var deleteRequest DeleteRequest
	decodeJson(&deleteRequest, request.Body)

	pusherClient := createPusherClient()

	var waitGroup sync.WaitGroup
	for index := range deleteRequest.Gists {
		waitGroup.Add(1)
		go func(index int) {
			gist := deleteRequest.Gists[index]

			deleteUrl := os.Getenv("GITHUB_REST_API_ENDPOINT") + "/gists/" + gist.ID
			apiRequest := createDeleteRequest(deleteUrl, deleteRequest.AccessToken)
			apiResponse := doHttpRequest(apiRequest)

			if apiResponse.StatusCode == http.StatusNoContent {
				sendPusherTrigger(
					pusherClient,
					deleteRequest.Id,
					EVENT_GIST_DELETED,
					map[string]string{
						"file_name": gist.FileName,
					},
				)
			}

			waitGroup.Done()
		}(index)
	}

	waitGroup.Wait()

	sendPusherTrigger(pusherClient, deleteRequest.Id, EVENT_ALL_GISTS_DELETED, nil)

	writeResponseObject(responseWriter, map[string]interface{}{})
}

func doHttpRequest(request *http.Request) *http.Response {
	client := &http.Client{}

	apiResponse, err := client.Do(request)
	if err != nil {
		log.Panicln("Cannot Perform Request", err.Error(), request.URL.String())
	}

	return apiResponse
}

func decodeJson(variable interface{}, reader io.Reader) interface{} {
	decoder := json.NewDecoder(reader)
	err := decoder.Decode(variable)
	if err != nil {
		contents, _ := ioutil.ReadAll(reader)
		log.Panicln("Cannot Decode Object", err.Error(), string(contents))
	}

	return variable
}

func sendPusherTrigger(pusherClient pusher.Client, channelName string, eventName string, data interface{}) {
	err := pusherClient.Trigger(channelName, eventName, data)
	if err != nil {
		log.Panicln("Cannot send pusher trigger", err.Error())
	}
}

func createPusherClient() pusher.Client {
	return pusher.Client{
		AppID:   os.Getenv("PUSHER_APP_ID"),
		Key:     os.Getenv("PUSHER_APP_KEY"),
		Secret:  os.Getenv("PUSHER_SECRET"),
		Cluster: os.Getenv("PUSHER_CLUSTER"),
		Secure:  true,
	}
}

func handleDashboard(responseWriter http.ResponseWriter, request *http.Request) {
	var dashboardRequest DashboardRequest
	decodeJson(&dashboardRequest, request.Body)

	accessToken := dashboardRequest.AccessToken

	if accessToken == ""  {
		accessToken = getAccessTokenFromCode(dashboardRequest.Code)
	}

	apiRequest := createGraphQlRequest(os.Getenv("GITHUB_GRAPHQL_API"), DASHBOARD_DATA_QUERY, accessToken)
	apiResponse := doHttpRequest(apiRequest)

	var apiResponseData DashboardData
	decodeJson(&apiResponseData, apiResponse.Body)

	dashboardDataObject := map[string]interface{}{
		"access_token":  accessToken,
		"error":         false,
		"is_successful": apiResponseData.Data.Viewer.Id != "",
		"id":            apiResponseData.Data.Viewer.Id,
		"username":      apiResponseData.Data.Viewer.Login,
		"avatar_url":    apiResponseData.Data.Viewer.AvatarURL,
		"name":          apiResponseData.Data.Viewer.Name,
		"bio":           apiResponseData.Data.Viewer.Bio,
		"url":           apiResponseData.Data.Viewer.URL,
		"gists":         getGistsFromEdges(apiResponseData),
	}

	writeResponseObject(responseWriter, dashboardDataObject)
}

func getAccessTokenFromCode(accessCode string) string {
	requestParams := map[string]interface{}{
		"client_id":     os.Getenv("GITHUB_CLIENT_ID"),
		"client_secret": os.Getenv("GITHUB_CLIENT_SECRET"),
		"code":          accessCode,
	}

	apiRequest := createPostRequest(os.Getenv("GITHUB_ACCESS_TOKEN_ENDPOINT"), requestParams)
	apiResponse := doHttpRequest(apiRequest)

	var githubApiResponse accessTokenResponse
	decodeJson(&githubApiResponse, apiResponse.Body)

	return githubApiResponse.AccessToken
}

func getGistsFromEdges(apiResponseData DashboardData) []HashMap {
	gists := make([]HashMap, 0)
	gistData := apiResponseData.Data.Viewer.Gists.Edges

	for index := range gistData {
		nodeData := gistData[index].Node
		gists = append(gists, map[string]interface{}{
			"id":          nodeData.ID,
			"file_name":   nodeData.Files[0].Name,
			"description": nodeData.Description,
			"timestamp":   strconv.FormatInt(nodeData.UpdatedAt.Unix(), 10),
			"name":        nodeData.Name,
			"url":         nodeData.URL,
			"is_public":   nodeData.IsPublic,
		})
	}

	return gists
}

func createDeleteRequest(url string, token string) *http.Request {
	apiRequest, err := http.NewRequest("DELETE", url, bytes.NewBuffer([]byte{}))
	if err != nil {
		log.Panicln("Cannot create request", err.Error(), url)
	}
	apiRequest.Header.Set("Accept", JSON_CONTENT_TYPE)
	apiRequest.Header.Set("Content-Type", JSON_CONTENT_TYPE)
	apiRequest.Header.Set("Authorization", "token "+token)

	return apiRequest
}

func createPostRequest(url string, requestParams map[string]interface{}) *http.Request {
	apiRequest, err := http.NewRequest("POST", url, bytes.NewBuffer(jsonEncodeMap(requestParams)))
	if err != nil {
		log.Panicln("Cannot create request", err.Error(), url)
	}
	apiRequest.Header.Set("Accept", JSON_CONTENT_TYPE)
	apiRequest.Header.Set("Content-Type", JSON_CONTENT_TYPE)

	return apiRequest
}

func createGraphQlRequest(url string, query string, token string) *http.Request {
	apiRequest, err := http.NewRequest("POST", url, bytes.NewBuffer([]byte(query)))

	if err != nil {
		log.Panicln("Cannot create request", err.Error(), url)
	}

	apiRequest.Header.Set("Authorization", "bearer "+token)
	apiRequest.Header.Set("Accept", JSON_CONTENT_TYPE)
	apiRequest.Header.Set("Content-Type", JSON_CONTENT_TYPE)

	return apiRequest
}

func jsonEncodeMap(mapObject map[string]interface{}) []byte {
	encodedObject, _ := json.Marshal(mapObject)
	return encodedObject
}

func writeResponseObject(responseWriter http.ResponseWriter, responseObject map[string]interface{}) {
	responseWriter.Header().Set("Content-Type", JSON_CONTENT_TYPE)
	responseWriter.Header().Set("Access-Control-Allow-Origin", "*")
	responseWriter.Header().Set("Access-Control-Allow-Methods", "POST, DELETE")
	responseWriter.Header().Set("Access-Control-Allow-Headers", "Content-Type")

	responseWriter.WriteHeader(http.StatusOK)

	responseObjectInBytes, err := json.Marshal(responseObject)
	if err != nil {
		log.Fatalln("Cannot convert object to string")
	}

	_, err = responseWriter.Write(responseObjectInBytes)
	if err != nil {
		log.Fatalln("Cannot write response")
	}
}

func loadEnvVariables() {
	err := godotenv.Load()
	if err != nil {
		log.Fatalln("Error loading .env file", err.Error())
	}
}

func init()  {
	loadEnvVariables()
}

func main() {
	bugsnag.Configure(bugsnag.Configuration{
		APIKey: os.Getenv("BUGSNAG_API_KEY"),
		// The import paths for the Go packages containing your source files
		ProjectPackages: []string{"main", os.Getenv("BUGSNAG_PROJECT_PACKAGES")},
	})

	log.Println("Server Started")

	router := mux.NewRouter()

	apiRouter := router.PathPrefix("/" + API_VERSION).Subrouter()
	apiRouter.HandleFunc("/dashboard", handleDashboard).Methods(http.MethodPost)
	apiRouter.HandleFunc("/delete", handleDelete).Methods(http.MethodDelete)

	router.PathPrefix("/").HandlerFunc(handleCatchAll)

	log.Fatalln(http.ListenAndServe(":8080", bugsnag.Handler(router)))
}