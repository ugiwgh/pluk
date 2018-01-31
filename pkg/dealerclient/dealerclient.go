package dealerclient

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Sirupsen/logrus"
)

type Client struct {
	Client    *http.Client
	BaseURL   *url.URL
	UserAgent string

	auth *AuthOpts
}

type AuthOpts struct {
	Token   string
	Cookie  string
	Headers http.Header
}

type Dataset struct {
	DisplayName   string
	Name          string
	Published     bool
	WorkspaceName string
}

func NewClient(baseURL string, auth *AuthOpts) (*Client, error) {
	baseURL = strings.TrimSuffix(baseURL, "/")
	base, err := url.Parse(baseURL)
	if err != nil {
		return nil, err
	}

	if auth.Headers != nil {
		hd := make(http.Header)
		for k, v := range auth.Headers {
			if k == "Authorization" || k == "Cookie" || k == "X-Workspace-Name" || k == "X-Workspace-Secret" {
				hd[k] = v
			}
		}
		auth.Headers = hd
	}

	base.Path = "/api/v0.2"
	baseClient := &http.Client{Timeout: time.Minute * 10}
	return &Client{
		BaseURL:   base,
		Client:    baseClient,
		UserAgent: "go-dealerclient/1",
		auth:      auth,
	}, nil
}

func (c *Client) NewRequest(method, urlStr string, body interface{}) (*http.Request, error) {
	u := c.BaseURL.String()
	u = strings.TrimSuffix(u, "/") + urlStr

	var buf io.ReadWriter
	if body != nil {
		buf = new(bytes.Buffer)
		err := json.NewEncoder(buf).Encode(body)
		if err != nil {
			return nil, err
		}
	}

	req, err := http.NewRequest(method, u, buf)
	if err != nil {
		return nil, err
	}
	if c.auth != nil {
		if c.auth.Headers != nil {
			req.Header = c.auth.Headers
		}
		if c.auth.Cookie != "" {
			req.Header.Set("Cookie", c.auth.Cookie)
		}
		if c.auth.Token != "" {
			req.Header.Set("Authorization", fmt.Sprintf("Bearer %v", c.auth.Token))
		}
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "*/*")
	if c.UserAgent != "" {
		req.Header.Set("User-Agent", c.UserAgent)
	}
	return req, nil
}

// Do sends an API request and returns the API response.  The API response is
// JSON decoded and stored in the value pointed to by v, or returned as an
// error if an API error has occurred.  If v implements the io.Writer
// interface, the raw response body will be written to v, without attempting to
// first decode it.
func (c *Client) Do(req *http.Request, v interface{}) (*http.Response, error) {
	logrus.Debugf("[go-dealerclient] %v %v", req.Method, req.URL)
	resp, err := c.Client.Do(req)
	if err != nil {
		if e, ok := err.(*url.Error); ok {
			return nil, e
		}
		return nil, err
	}

	defer func() {
		// Drain up to 512 bytes and close the body to let the Transport reuse the connection
		io.CopyN(ioutil.Discard, resp.Body, 512)
		resp.Body.Close()
	}()

	if resp, err = checkResponse(resp, err); err != nil {
		return resp, err
	}
	if v != nil {
		if w, ok := v.(io.Writer); ok {
			io.Copy(w, resp.Body)
		} else {
			err = json.NewDecoder(resp.Body).Decode(v)
			if err == io.EOF {
				err = nil // ignore EOF errors caused by empty response body
			}
		}
	}

	return resp, err
}

func (c *Client) CreateDataset(workspace, name string) error {
	u := fmt.Sprintf("/workspace/%v/datasets", workspace)

	ds := &Dataset{Name: name, WorkspaceName: workspace, Published: true, DisplayName: strings.Title(name)}
	req, err := c.NewRequest("POST", u, ds)
	if err != nil {
		return err
	}
	_, err = c.Do(req, nil)

	if err != nil {
		return err
	}
	return nil
}

func (c *Client) ListDatasets(workspace string) ([]Dataset, error) {
	u := fmt.Sprintf("/workspace/%v/datasets", workspace)

	var ds = make([]Dataset, 0)
	req, err := c.NewRequest("GET", u, nil)
	if err != nil {
		return nil, err
	}
	_, err = c.Do(req, &ds)

	if err != nil {
		return nil, err
	}
	return ds, nil
}

func checkResponse(resp *http.Response, err error) (*http.Response, error) {
	if err != nil || resp.StatusCode >= 400 {
		if err != nil {
			return &http.Response{StatusCode: http.StatusInternalServerError}, err
		} else {
			messageBytes, _ := ioutil.ReadAll(resp.Body)
			message := strconv.Itoa(resp.StatusCode) + ": " + string(messageBytes)
			return resp, errors.New(message)
		}
	}
	return resp, nil
}