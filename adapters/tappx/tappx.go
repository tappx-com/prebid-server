package tappx

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"text/template"
	"time"

	"github.com/prebid/openrtb/v20/openrtb2"
	"github.com/prebid/prebid-server/v3/adapters"
	"github.com/prebid/prebid-server/v3/config"
	"github.com/prebid/prebid-server/v3/errortypes"
	"github.com/prebid/prebid-server/v3/macros"
	"github.com/prebid/prebid-server/v3/openrtb_ext"
	"github.com/prebid/prebid-server/v3/util/jsonutil"
)

const TAPPX_BIDDER_VERSION = "1.6"
const TYPE_CNN = "prebid"

type TappxAdapter struct {
	endpointTemplate *template.Template
}

type Bidder struct {
	Tappxkey string   `json:"tappxkey"`
	Mktag    string   `json:"mktag,omitempty"`
	Bcid     []string `json:"bcid,omitempty"`
	Bcrid    []string `json:"bcrid,omitempty"`
}

type Ext struct {
	Bidder `json:"bidder"`
	GPID   string `json:"gpid,omitempty"`
}

// Builder builds a new instance of the Tappx adapter for the given bidder with the given config.
func Builder(bidderName openrtb_ext.BidderName, config config.Adapter, server config.Server) (adapters.Bidder, error) {
	template, err := template.New("endpointTemplate").Parse(config.Endpoint)
	if err != nil {
		return nil, fmt.Errorf("unable to parse endpoint url template: %v", err)
	}

	bidder := &TappxAdapter{
		endpointTemplate: template,
	}
	return bidder, nil
}

func (a *TappxAdapter) MakeRequests(request *openrtb2.BidRequest, reqInfo *adapters.ExtraRequestInfo) ([]*adapters.RequestData, []error) {
	if len(request.Imp) == 0 {
		return nil, []error{&errortypes.BadInput{
			Message: "No impression in the bid request",
		}}
	}

	var bidderExt adapters.ExtImpBidder
	if err := jsonutil.Unmarshal(request.Imp[0].Ext, &bidderExt); err != nil {
		return nil, []error{&errortypes.BadInput{
			Message: "Error parsing bidderExt object",
		}}
	}
	var tappxExt openrtb_ext.ExtImpTappx
	if err := jsonutil.Unmarshal(bidderExt.Bidder, &tappxExt); err != nil {
		return nil, []error{&errortypes.BadInput{
			Message: "Error parsing tappxExt parameters",
		}}
	}

	ext := Ext{
		Bidder: Bidder{
			Tappxkey: tappxExt.TappxKey,
			Mktag:    tappxExt.Mktag,
			Bcid:     tappxExt.Bcid,
			Bcrid:    tappxExt.Bcrid,
		},
	}

	if jsonext, err := json.Marshal(ext); err == nil {
		request.Ext = jsonext
	} else {
		return nil, []error{&errortypes.FailedToRequestBids{
			Message: "Error marshaling tappxExt parameters",
		}}
	}

	test := int(request.Test)

	url, err := a.buildEndpointURL(&tappxExt, test)
	if url == "" {
		return nil, []error{err}
	}

	if tappxExt.BidFloor > 0 {
		request.Imp[0].BidFloor = tappxExt.BidFloor
	}

	reqJSON, err := json.Marshal(request)
	if err != nil {
		return nil, []error{&errortypes.BadInput{
			Message: "Error parsing reqJSON object",
		}}
	}

	headers := http.Header{}
	headers.Add("Content-Type", "application/json;charset=utf-8")
	headers.Add("Accept", "application/json")
	return []*adapters.RequestData{{
		Method:  "POST",
		Uri:     url,
		Body:    reqJSON,
		Headers: headers,
		ImpIDs:  openrtb_ext.GetImpIDs(request.Imp),
	}}, []error{}
}

// Builds enpoint url based on adapter-specific pub settings from imp.ext
func (a *TappxAdapter) buildEndpointURL(params *openrtb_ext.ExtImpTappx, test int) (string, error) {

	if params.Endpoint == "" {
		return "", &errortypes.BadInput{
			Message: "Tappx endpoint undefined",
		}
	}

	if params.TappxKey == "" {
		return "", &errortypes.BadInput{
			Message: "Tappx key undefined",
		}
	}

	isNewEndpoint, err := regexp.Match(`^(zz|vz)[0-9]{3,}([a-z]{2,3}|test)$`, []byte(params.Endpoint))
	if err != nil {
		return "", &errortypes.BadInput{
			Message: "Unable to match params.Endpoint " + string(params.Endpoint) + "): " + err.Error(),
		}
	}
	var tappxHost string
	if isNewEndpoint {
		tappxHost = params.Endpoint + ".pub.tappx.com/rtb/"
	} else {
		tappxHost = "ssp.api.tappx.com/rtb/v2/"
	}

	endpointParams := macros.EndpointTemplateParams{Host: tappxHost}
	host, err := macros.ResolveMacros(a.endpointTemplate, endpointParams)

	if err != nil {
		return "", &errortypes.BadInput{
			Message: "Unable to parse endpoint url template: " + err.Error(),
		}
	}

	thisURI, err := url.Parse(host)
	if err != nil {
		return "", &errortypes.BadInput{
			Message: "Malformed URL: " + err.Error(),
		}
	}

	if !isNewEndpoint {
		thisURI.Path += params.Endpoint
	}

	queryParams := url.Values{}

	queryParams.Add("tappxkey", params.TappxKey)

	if test == 0 {
		t := time.Now().UnixNano() / (int64(time.Millisecond) / int64(time.Nanosecond))
		queryParams.Add("ts", strconv.Itoa(int(t)))
	}

	queryParams.Add("v", TAPPX_BIDDER_VERSION)

	queryParams.Add("type_cnn", TYPE_CNN)

	thisURI.RawQuery = queryParams.Encode()

	return thisURI.String(), nil
}

func (a *TappxAdapter) MakeBids(internalRequest *openrtb2.BidRequest, externalRequest *adapters.RequestData, response *adapters.ResponseData) (*adapters.BidderResponse, []error) {
	if response.StatusCode == http.StatusNoContent {
		return nil, nil
	}

	if response.StatusCode == http.StatusBadRequest {
		return nil, []error{&errortypes.BadInput{
			Message: fmt.Sprintf("Unexpected status code: %d. Run with request.debug = 1 for more info", response.StatusCode),
		}}
	}

	if response.StatusCode != http.StatusOK {
		return nil, []error{fmt.Errorf("Unexpected status code: %d. Run with request.debug = 1 for more info", response.StatusCode)}
	}

	var bidResp openrtb2.BidResponse
	if err := jsonutil.Unmarshal(response.Body, &bidResp); err != nil {
		return nil, []error{err}
	}

	bidResponse := adapters.NewBidderResponseWithBidsCapacity(5)

	for _, sb := range bidResp.SeatBid {
		for i := 0; i < len(sb.Bid); i++ {
			bid := sb.Bid[i]
			bidResponse.Bids = append(bidResponse.Bids, &adapters.TypedBid{
				Bid:     &bid,
				BidType: getMediaTypeForImp(bid.ImpID, internalRequest.Imp),
			})

		}
	}
	return bidResponse, []error{}
}

func getMediaTypeForImp(impId string, imps []openrtb2.Imp) openrtb_ext.BidType {
	mediaType := openrtb_ext.BidTypeBanner
	for _, imp := range imps {
		if imp.ID == impId {
			if imp.Video != nil {
				mediaType = openrtb_ext.BidTypeVideo
			}
			return mediaType
		}
	}
	return mediaType
}
