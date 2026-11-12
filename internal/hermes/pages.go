package hermes

import (
	"net/http"
	"path/filepath"
)

// pages.go: friendly addresses for the phone pages.
//
//   /lists, /lists/todo, /lists/shoppinglist (/lists/<name>) → lists.html,
//     which opens that list (lists.js reads the path). Open to the lists
//     token like /lists.html.
//   /documents → documents.html: upload files from a phone or another PC
//     and see or delete the hub's documents (needs the full token on the
//     network, like the hub).

func servePage(name string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache")
		http.ServeFile(w, r, filepath.Join(envOr("STATIC_DIR", "web/static"), name))
	}
}

func init() {
	registerRoute("GET /lists", servePage("lists.html"))
	registerRoute("GET /lists/{name}", servePage("lists.html"))
	registerRoute("GET /documents", servePage("documents.html"))
	registerRoute("GET /dokumente", servePage("documents.html"))
}
