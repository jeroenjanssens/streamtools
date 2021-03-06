package library

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/nytlabs/streamtools/st/blocks" // blocks
)

// specify those channels we're going to use to communicate with streamtools
type FromHTTPStream struct {
	blocks.Block
	queryrule chan blocks.MsgChan
	inrule    blocks.MsgChan
	in        blocks.MsgChan
	out       blocks.MsgChan
	quit      blocks.MsgChan
}

// a bit of boilerplate for streamtools
func NewFromHTTPStream() blocks.BlockInterface {
	return &FromHTTPStream{}
}

func (b *FromHTTPStream) Setup() {
	b.Kind = "Network I/O"
	b.Desc = "emits new data appearing on a long-lived http stream as new messages in streamtools"
	b.inrule = b.InRoute("rule")
	b.queryrule = b.QueryRoute("rule")
	b.quit = b.Quit()
	b.out = b.Broadcast()
}

func listen(b *FromHTTPStream, endpoint string, auth string, dataChan chan interface{}, quitChan chan bool) {
	transport := http.Transport{
		Dial: dialTimeout,
	}

	client := &http.Client{
		Transport: &transport,
	}
	var res *http.Response
	var body bytes.Buffer
	// these are the possible delimiters
	d1 := []byte{125, 10} // this is }\n
	d2 := []byte{13, 10}  // this is CRLF
	req, err := http.NewRequest("GET", endpoint, nil)
	if err != nil {
		b.Error(err)
		goto WaitForDeath
	}
	if len(auth) > 0 {
		req.SetBasicAuth(strings.Split(auth, ":")[0], strings.Split(auth, ":")[1])
	}
	res, err = client.Do(req)
	if err != nil {
		b.Error(err)
		goto WaitForDeath
	}
	defer res.Body.Close()
Loop:
	for {
		select {
		case <-quitChan:
			res.Body.Close()
			return
		default:
			buffer := make([]byte, 5*1024)
			p, err := res.Body.Read(buffer)

			if err != nil && err.Error() == "EOF" {
				b.Error(err)
				break Loop
			}

			if err != nil {
				b.Error(err)
				continue
			}
			// catch odd little buffers
			if p < 2 {
				break
			}
			body.Write(buffer[:p])

			if bytes.Equal(d1, buffer[p-2:p]) || bytes.Equal(d2, buffer[p-2:p]) { // ended with }\n
				for _, blob := range bytes.Split(body.Bytes(), []byte{10}) { // split on new line in case there are multuple messages per buffer
					if len(blob) > 0 {
						var outMsg interface{}
						err := json.Unmarshal(blob, &outMsg)
						// if the json parsing fails, store data unparsed as "data"
						if err != nil {
							outMsg = map[string]interface{}{
								"data": blob,
							}
						}
						select {
						case dataChan <- outMsg:
						default:
							b.Error(errors.New("Discarding " + string(len(dataChan)) + "messages"))
							continue
						}
					}
				}
				body.Reset()
			}
		}
	}
WaitForDeath:
	<-quitChan
}

// creates a persistent HTTP connection, emitting all messages from
// the stream into streamtools
func (b *FromHTTPStream) Run() {
	var endpoint string
	var ok bool
	var auth string
	// channels for the listener
	dataChan := make(chan interface{}, 1000)
	var quitChan chan bool

	for {
		select {
		case ruleI := <-b.inrule:

			if quitChan != nil {
				quitChan <- true
			}

			rule := ruleI.(map[string]interface{})
			endpoint, ok = rule["Endpoint"].(string)
			if !ok {
				b.Error("bad endpoint")
				break
			}
			tauth, ok := rule["Auth"]
			if !ok {
				tauth = ""
			}
			auth, ok = tauth.(string)
			if !ok {
				b.Error("bad auth")
				break
			}

			quitChan = make(chan bool)
			go listen(b, endpoint, auth, dataChan, quitChan)

		case c := <-b.queryrule:
			c <- map[string]interface{}{
				"Endpoint": endpoint,
				"Auth":     auth,
			}
		case <-b.quit:
			if quitChan != nil {
				quitChan <- true
			}
			return
		case msg := <-dataChan:
			b.out <- msg
		}
	}
}
