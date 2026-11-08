// SPDX-License-Identifier: MIT

package transcript

import (
	"net/url"
	"sort"
	"strings"
)

// collections maps the first path segment of the API to the name of the
// parameter of its object ID in the specification.
var collections = map[string]string{
	"programs":        "programID",
	"events":          "eventID",
	"reports":         "reportID",
	"subscriptions":   "subscriptionID",
	"vens":            "venID",
	"resources":       "resourceID",
	"resource_groups": "resourceGroupID",
}

// Kind returns the kind of object a route is about ("programs", or
// "vens/resources" for the resources of a VEN), and "" for the others.
func Kind(route string) string {
	parts := strings.Split(strings.Trim(route, "/"), "/")
	if _, ok := collections[parts[0]]; !ok {
		return ""
	}
	if parts[0] == "vens" && len(parts) >= 3 && parts[2] == "resources" {
		return "vens/resources"
	}
	return parts[0]
}

// Route returns the path template of the specification a path matches:
// /programs/{programID}, /vens/{venID}/resources/{resourceID},
// /notifiers/mqtt/topics/programs/{programID}. A path the API does not
// have is returned as it is.
func Route(path string) string {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) == 0 || parts[0] == "" {
		return "/"
	}
	out := make([]string, len(parts))
	copy(out, parts)
	switch {
	case collections[parts[0]] != "":
		if len(parts) >= 2 {
			out[1] = "{" + collections[parts[0]] + "}"
		}
		if parts[0] == "vens" && len(parts) >= 4 && parts[2] == "resources" {
			out[3] = "{resourceID}"
		}
	case parts[0] == "notifiers" && len(parts) >= 5 && parts[1] == "mqtt" && parts[2] == "topics":
		// /notifiers/mqtt/topics/{collection}/{id}[/...]
		if p, ok := collections[parts[3]]; ok {
			out[4] = "{" + p + "}"
		}
		if parts[3] == "vens" && len(parts) >= 7 && parts[5] == "resources" {
			out[6] = "{resourceID}"
		}
	}
	return "/" + strings.Join(out, "/")
}

func decodeForm(s string) (map[string][]string, error) {
	v, err := url.ParseQuery(s)
	if err != nil {
		return nil, err
	}
	return v, nil
}

// encodeForm encodes form fields in sorted order, as url.Values does.
func encodeForm(form map[string][]string) string {
	keys := make([]string, 0, len(form))
	for k := range form {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	v := url.Values{}
	for _, k := range keys {
		v[k] = form[k]
	}
	return v.Encode()
}
