package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"reflect"
	"strconv"
	"strings"

	"cloud.google.com/go/storage"
	"github.com/pborman/uuid"
	elastic "gopkg.in/olivere/elastic.v3"
)

type Location struct {
      Lat float64 `json:"lat"`
      Lon float64 `json:"lon"`
}

type Post struct {
      // `json:"user"` is for the json parsing of this User field. 
	  // Otherwise, by default it's 'User'.
      User     string `json:"user"`
      Message  string  `json:"message"`
      Location Location `json:"location"`
	  Url    string `json:"url"`
}


var ES_URL string = fmt.Sprintf("http://%s:9200/", os.Getenv("ES_URL"))
var BUCKET_NAME string =  os.Getenv("BUCKET_NAME")

const (
	INDEX = "around"
	TYPE = "post"
	DISTANCE = "200km"
)


func main() {
	
	client, err := elastic.NewClient(elastic.SetURL(ES_URL), elastic.SetSniff(false))
	if err != nil {
		panic(err)
		return
	}

	exists, err := client.IndexExists(INDEX).Do()
	if err != nil {
		panic(err)
	}

	if !exists {

		mapping := `{
			"mappings":{
				"post":{
					"properties":{
						"location":{
							"type":"geo_point"
						}
					}
				}
			}
		}`
		_, err := client.CreateIndex(INDEX).Body(mapping).Do()
		if err != nil {
			panic(err)
		}
	}

      fmt.Println("started-service")
      http.HandleFunc("/post", handlerPost)
	  http.HandleFunc("/search", handlerSearch)
      log.Fatal(http.ListenAndServe(":8080", nil))
}

func containsFilteredWords(s *string) bool {
	filteredWords := []string{
			"fuck",
			"damned",
			"hell",
			"shit",
	}

	for _, word := range filteredWords {
		if strings.Contains(*s, word) {
			return true
		}
	}

	return false
}

func handlerPost(w http.ResponseWriter, r *http.Request) {
	// ======Old version of code : json data
	// fmt.Println("Received one post request")
	// decoder := json.NewDecoder(r.Body)
	// var p Post
	// if err := decoder.Decode(&p); err != nil {
	// 	   panic(err)
	// 	   return
	// }
	// fmt.Fprintf(w, "Post received: %s\n", p.Message)

	// ======New : multi-form =========
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type,Authorization")

	// 32MB  << means shift left 20 after decimal  1K = 1024  1MB = 1024*1024
	r.ParseMultipartForm(32 << 20)
	
	// parse form data - text
	fmt.Printf("Received one post request %s\n", r.FormValue("message"))
	lat, _ := strconv.ParseFloat(r.FormValue("lat"), 64)
	lon, _ := strconv.ParseFloat(r.FormValue("lon"), 64)

	p := &Post{
		   User:    "1111",
		   Message: r.FormValue("message"),
		   Location: Location{
				  Lat: lat,
				  Lon: lon,
		   },
	}

	id := uuid.New()

	// parse form data - image  
	file, _, err := r.FormFile("image")
	if err != nil {
		http.Error(w, "Image is not available", http.StatusInternalServerError)
		fmt.Printf("Image is not available %v.\n", err)
		return
 }
 	defer file.Close()

	// use context, saveToGCS
	ctx := context.Background()
	_, attrs, err := saveToGCS(ctx, file, BUCKET_NAME, id)
      if err != nil {
             http.Error(w, "GCS is not setup", http.StatusInternalServerError)
             fmt.Printf("GCS is not setup %v\n", err)
             return
      }
	// update p with the newly generated link of image
	p.Url = attrs.MediaLink

    saveToES(p, id)

    // saveToBigTable(p, id)
}
	

// Save an image to GCS.
func saveToGCS(ctx context.Context, r io.Reader, bucketName, name string) (*storage.ObjectHandle, *storage.ObjectAttrs, error) {
	client, err := storage.NewClient(ctx)
	if err != nil {
		   return nil, nil, err
	}
	defer client.Close()

	bucket := client.Bucket(bucketName)
	// Next check if the bucket exists
	if _, err = bucket.Attrs(ctx); err != nil {
		   return nil, nil, err
	}

	obj := bucket.Object(name)
	w := obj.NewWriter(ctx)
	if _, err := io.Copy(w, r); err != nil {
		   return nil, nil, err
	}
	if err := w.Close(); err != nil {
		   return nil, nil, err
	}
	
	if err := obj.ACL().Set(ctx, storage.AllUsers, storage.RoleReader); err != nil {
		   return nil, nil, err
	}

	attrs, err := obj.Attrs(ctx)
	fmt.Printf("Post is saved to GCS: %s\n", attrs.MediaLink)
	return obj, attrs, err
}

// Save a post to ElasticSearch
func saveToES(p *Post, id string) {
	// Create a client
	es_client, err := elastic.NewClient(elastic.SetURL(ES_URL), elastic.SetSniff(false))
	if err != nil {
		panic(err)
		return
	}

	// Save it to index
	_, err = es_client.Index().
		Index(INDEX).
		Type(TYPE).
		Id(id).
		BodyJson(p).
		Refresh(true).
		Do()
	if err != nil {
		panic(err)
		return
	}

	fmt.Printf("Post is saved to Index: %s\n", p.Message)
}


func handlerSearch(w http.ResponseWriter, r *http.Request) {
	fmt.Println("Received one request for search")

	lat, _ := strconv.ParseFloat(r.URL.Query().Get("lat"), 64)
	lon, _ := strconv.ParseFloat(r.URL.Query().Get("lon"), 64)
	ran := DISTANCE 
	if val := r.URL.Query().Get("range"); val != "" { 
		ran = val + "km" 
	}

	fmt.Printf("Search Received: %f %f %s\n", lat, lon, ran)

	client, err := elastic.NewClient(elastic.SetURL(ES_URL), elastic.SetSniff(false))
	if err != nil {
		panic(err)
	}

	q := elastic.NewGeoDistanceQuery("location")
	q = q.Distance(ran).Lat(lat).Lon(lon)

	searchResult, err := client.Search().Index(INDEX).Query(q).Pretty(true).Do()
	if err != nil {
		panic(err)
	}
	fmt.Printf("Query took %d ms\n", searchResult.TookInMillis)
	fmt.Printf("Found a total of %d posts\n",  searchResult.TotalHits() )

	var typ Post
	var ps []Post
	for _, item := range searchResult.Each(reflect.TypeOf(typ)) {
		p := item.(Post)

		fmt.Printf("Post by %s: %s at lat %v and lon %v\n", 
		p.User, p.Message, p.Location.Lat, p.Location.Lon)
		// filter
		if !containsFilteredWords(&p.Message) {
			ps = append(ps, p)
		}


	}

	js, err := json.Marshal(ps)
	if err != nil{
		panic(err)
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Write(js)
}