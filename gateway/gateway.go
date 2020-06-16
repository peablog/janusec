/*
 * @Copyright Reserved By Janusec (https://www.janusec.com/).
 * @Author: U2
 * @Date: 2018-07-14 16:37:57
 * @Last Modified: U2, 2018-07-14 16:37:57
 */

package gateway

import (
	"compress/gzip"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"net"
	"net/http"
	"net/http/httputil"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Janusec/janusec/backend"
	"github.com/Janusec/janusec/data"
	"github.com/Janusec/janusec/firewall"
	"github.com/Janusec/janusec/models"
	"github.com/Janusec/janusec/usermgmt"
	"github.com/Janusec/janusec/utils"
	"github.com/gorilla/sessions"
	"github.com/patrickmn/go-cache"
	"github.com/yookoala/gofast"
	"golang.org/x/net/http2"
)

var (
	store  = sessions.NewCookieStore([]byte("janusec"))
	gzPool = sync.Pool{
		New: func() interface{} {
			w := gzip.NewWriter(ioutil.Discard)
			return w
		},
	}
)

type gzipResponseWriter struct {
	io.Writer
	http.ResponseWriter
}

func (w *gzipResponseWriter) WriteHeader(status int) {
	w.Header().Del("Content-Length")
	w.ResponseWriter.WriteHeader(status)
}

func (w *gzipResponseWriter) Write(b []byte) (int, error) {
	return w.Writer.Write(b)
}

// ReverseHandlerFunc used for reverse handler
func ReverseHandlerFunc(w http.ResponseWriter, r *http.Request) {
	//fmt.Println("Gateway ReverseHandlerFunc", r.Host)
	domain := backend.GetDomainByName(r.Host)
	if domain != nil && domain.Redirect == true {
		RedirectRequest(w, r, domain.Location)
		return
	}
	app := backend.GetApplicationByDomain(r.Host)
	if app == nil {
		hitInfo := &models.HitInfo{PolicyID: 0, VulnName: "Unknown Host"}
		GenerateBlockPage(w, hitInfo)
		return
	}
	if (r.TLS == nil) && (app.RedirectHTTPS == true) {
		RedirectRequest(w, r, "https://"+r.Host+r.URL.Path)
		return
	}
	if domain == nil{
		http.ServeFile(w, r, "./cdn_static_files/warning.html")
		return
	}
	r.URL.Scheme = app.InternalScheme
	r.URL.Host = r.Host
	//Cache
	appidStr := strconv.Itoa(int(app.ID))
	//fmt.Println("ReverseHandlerFunc, r.URL.Path:", r.URL.Path)
	isStatic := backend.IsStaticDir(domain.Name, r.URL.Path)
	fmt.Println("is_static:", isStatic)
	if r.Method == "GET" && isStatic {
		staticRoot := "./cdn_static_files/" + appidStr + "/"
		if strings.HasSuffix(r.URL.Path, "/") {
			http.ServeFile(w, r, "./cdn_static_files/warning.html")
			return
		}
		staticHandler := http.FileServer(http.Dir(staticRoot))
		if !strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
			fmt.Println("hit cache but no gzip:", r.URL.Path)
			staticHandler.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Content-Encoding", "gzip")
		w.Header().Set("Access-Control-Allow-Origin", "*")
		gz := gzPool.Get().(*gzip.Writer)
		defer gzPool.Put(gz)
		gz.Reset(w)
		defer gz.Close()
		staticHandler.ServeHTTP(&gzipResponseWriter{ResponseWriter: w, Writer: gz}, r)
		return
	}
	// dynamic
	srcIP := GetClientIP(r, app)
	if app.WAFEnabled && !firewall.IsStaticResource(r) {
		if isCC, ccPolicy, clientID, needLog := firewall.IsCCAttack(r, app.ID, srcIP); isCC == true {
			targetURL := r.URL.Path
			if len(r.URL.RawQuery) > 0 {
				targetURL += "?" + r.URL.RawQuery
			}
			hitInfo := &models.HitInfo{TypeID: 1,
				PolicyID:  ccPolicy.AppID,
				VulnName:  "CC",
				Action:    ccPolicy.Action,
				ClientID:  clientID,
				TargetURL: targetURL,
				BlockTime: time.Now().Unix()}
			switch ccPolicy.Action {
			case models.Action_Block_100:
				if needLog {
					go firewall.LogCCRequest(r, app.ID, srcIP, ccPolicy)
				}
				GenerateBlockPage(w, hitInfo)
				return
			case models.Action_BypassAndLog_200:
				if needLog {
					go firewall.LogCCRequest(r, app.ID, srcIP, ccPolicy)
				}
			case models.Action_CAPTCHA_300:
				if needLog {
					go firewall.LogCCRequest(r, app.ID, srcIP, ccPolicy)
				}
				captchaHitInfo.Store(hitInfo.ClientID, hitInfo)
				captchaURL := CaptchaEntrance + "?id=" + hitInfo.ClientID
				http.Redirect(w, r, captchaURL, http.StatusTemporaryRedirect)
				return
			}
		}

		if isHit, policy := firewall.IsRequestHitPolicy(r, app.ID, srcIP); isHit == true {
			switch policy.Action {
			case models.Action_Block_100:
				vulnName, _ := firewall.VulnMap.Load(policy.VulnID)
				hitInfo := &models.HitInfo{TypeID: 2, PolicyID: policy.ID, VulnName: vulnName.(string)}
				go firewall.LogGroupHitRequest(r, app.ID, srcIP, policy)
				GenerateBlockPage(w, hitInfo)
				return
			case models.Action_BypassAndLog_200:
				go firewall.LogGroupHitRequest(r, app.ID, srcIP, policy)
			case models.Action_CAPTCHA_300:
				go firewall.LogGroupHitRequest(r, app.ID, srcIP, policy)
				clientID := GenClientID(r, app.ID, srcIP)
				targetURL := r.URL.Path
				if len(r.URL.RawQuery) > 0 {
					targetURL += "?" + r.URL.RawQuery
				}
				hitInfo := &models.HitInfo{TypeID: 2,
					PolicyID: policy.ID, VulnName: "Group Policy Hit",
					Action: policy.Action, ClientID: clientID,
					TargetURL: targetURL, BlockTime: time.Now().Unix()}
				captchaHitInfo.Store(clientID, hitInfo)
				captchaURL := CaptchaEntrance + "?id=" + clientID
				http.Redirect(w, r, captchaURL, http.StatusTemporaryRedirect)
				return
			default:
				// models.Action_Pass_400 do nothing
			}
		}
	}

	// Check OAuth
	if app.OAuthRequired && data.CFG.MasterNode.OAuth.Enabled {
		session, _ := store.Get(r, "janusec-token")
		usernameI := session.Values["userid"]
		var url string
		if r.TLS != nil {
			url = "https://" + r.Host + r.URL.Path
		} else {
			url = r.URL.String()
		}
		//fmt.Println("1000", usernameI, url)
		if usernameI == nil {
			// Exec OAuth2 Authentication
			ua := r.UserAgent() //r.Header.Get("User-Agent")
			state := data.SHA256Hash(srcIP + url + ua)
			stateSession := session.Values[state]
			//fmt.Println("1001 state=", state, url)
			if stateSession == nil {
				entranceURL, err := getOAuthEntrance(state)
				if err != nil {
					w.Write([]byte(err.Error()))
					return
				}
				// Save Application URL for CallBack
				oauthState := models.OAuthState{
					CallbackURL: url,
					UserID:      ""}
				usermgmt.OAuthCache.Set(state, oauthState, cache.DefaultExpiration)
				session.Values[state] = state
				session.Options = &sessions.Options{Path: "/", MaxAge: 300}
				session.Save(r, w)
				//fmt.Println("1002 cache state:", oauthState, url, "307 to:", entranceURL)
				http.Redirect(w, r, entranceURL, http.StatusTemporaryRedirect)
				return
			}
			// Has state in session, get UserID from cache
			state = stateSession.(string)
			oauthStateI, found := usermgmt.OAuthCache.Get(state)
			if found == false {
				// Time expired, clear session
				session.Options = &sessions.Options{Path: "/", MaxAge: -1}
				session.Save(r, w)
				http.Redirect(w, r, url, http.StatusTemporaryRedirect)
				return
			}
			// found == true
			oauthState := oauthStateI.(models.OAuthState)
			if oauthState.UserID == "" {
				session.Values["userid"] = nil
				entranceURL, err := getOAuthEntrance(state)
				if err != nil {
					w.Write([]byte(err.Error()))
					return
				}
				http.Redirect(w, r, entranceURL, http.StatusTemporaryRedirect)
				return
			} else {
				session.Values["userid"] = oauthState.UserID
				session.Values["access_token"] = oauthState.AccessToken
			}
			session.Options = &sessions.Options{Path: "/", MaxAge: int(app.SessionSeconds)}
			session.Save(r, w)
			http.Redirect(w, r, oauthState.CallbackURL, http.StatusTemporaryRedirect)
			return
		}
		// Exist username in session, Forward username to destination
		accessToken := session.Values["access_token"].(string)
		r.Header.Set("Authorization", "Bearer "+accessToken)
		r.Header.Set("X-Auth-User", usernameI.(string))
	}

	dest := backend.SelectBackendRoute(app, r)

	//fmt.Println("dest", dest, dest.RouteType)

	if dest.RouteType == models.StaticRoute {
		// Static Web site
		staticHandler := http.FileServer(http.Dir(dest.BackendRoute))
		if strings.HasSuffix(r.URL.Path, "/") {
			http.ServeFile(w, r, dest.BackendRoute+r.URL.Path+dest.Destination)
			return
		}
		staticHandler.ServeHTTP(w, r)
		return
	} else if dest.RouteType == models.FastCGIRoute {
		// FastCGI
		connFactory := gofast.SimpleConnFactory("tcp", dest.Destination)
		urlPath := utils.GetRoutePath(r.URL.Path)
		newPath := r.URL.Path
		if urlPath != "/" {
			newPath = strings.Replace(r.URL.Path, dest.RequestRoute, "/", 1)
		}
		fastCGIHandler := gofast.NewHandler(
			gofast.NewFileEndpoint(dest.BackendRoute+newPath)(gofast.BasicSession),
			gofast.SimpleClientFactory(connFactory, 0),
		)
		fastCGIHandler.ServeHTTP(w, r)
		return
	}

	// Reverse Proxy
	// var transport http.RoundTripper
	transport := &http.Transport{
		TLSHandshakeTimeout:   10 * time.Second,
		IdleConnTimeout:       30 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			return net.Dial("tcp", dest.Destination)
		},
		DialTLS: func(network, addr string) (net.Conn, error) {
			conn, err := net.Dial("tcp", dest.Destination)
			if err != nil {
				return nil, err
			}
			cfg := &tls.Config{ServerName: r.Host, NextProtos: []string{"h2", "http/1.1"}}
			tlsConn := tls.Client(conn, cfg)
			if err := tlsConn.Handshake(); err != nil {
				conn.Close()
				return nil, err
			}
			return tlsConn, nil //net.Dial("tcp", dest)
		},
	}
	http2.ConfigureTransport(transport)
	proxy := &httputil.ReverseProxy{
		Director: func(req *http.Request) {
			//req.URL.Scheme = app.InternalScheme
			//req.URL.Host = r.Host
		},
		Transport:      transport,
		ModifyResponse: rewriteResponse}
	if utils.Debug {
		dump, err := httputil.DumpRequest(r, true)
		utils.CheckError("ReverseHandlerFunc DumpRequest", err)
		fmt.Println(string(dump))
	}
	proxy.ServeHTTP(w, r)
}

func getOAuthEntrance(state string) (entranceURL string, err error) {
	switch data.CFG.MasterNode.OAuth.Provider {
	case "wxwork":
		entranceURL = fmt.Sprintf("https://open.work.weixin.qq.com/wwopen/sso/qrConnect?appid=%s&agentid=%s&redirect_uri=%s&state=%s",
			data.CFG.MasterNode.OAuth.Wxwork.CorpID,
			data.CFG.MasterNode.OAuth.Wxwork.AgentID,
			data.CFG.MasterNode.OAuth.Wxwork.Callback,
			state)
	case "dingtalk":
		entranceURL = fmt.Sprintf("https://oapi.dingtalk.com/connect/qrconnect?appid=%s&response_type=code&scope=snsapi_login&state=%s&redirect_uri=%s",
			data.CFG.MasterNode.OAuth.Dingtalk.AppID,
			state,
			data.CFG.MasterNode.OAuth.Dingtalk.Callback)
	case "feishu":
		entranceURL = fmt.Sprintf("https://open.feishu.cn/open-apis/authen/v1/index?redirect_uri=%s&app_id=%s&state=%s",
			data.CFG.MasterNode.OAuth.Feishu.Callback,
			data.CFG.MasterNode.OAuth.Feishu.AppID,
			state)
	case "ldap":
		entranceURL = "/ldap/login?state=" + state
	default:
		//w.Write([]byte("Designated OAuth not supported, please check config.json ."))
		return "", errors.New("the OAuth provider is not supported, please check config.json")
	}
	return entranceURL, nil
}

// RedirectRequest for example: redirect 80 to 443
func RedirectRequest(w http.ResponseWriter, r *http.Request, location string) {
	if len(r.URL.RawQuery) > 0 {
		location += "?" + r.URL.RawQuery
	}
	http.Redirect(w, r, location, http.StatusMovedPermanently)
}

// GenClientID generate unique client id
func GenClientID(r *http.Request, appID int64, srcIP string) string {
	preHashContent := srcIP
	url := r.URL.Path
	preHashContent += url
	ua := r.Header.Get("User-Agent")
	preHashContent += ua
	cookie := r.Header.Get("Cookie")
	preHashContent += cookie
	clientID := data.SHA256Hash(preHashContent)
	return clientID
}

// GetClientIP acquire the client IP address
func GetClientIP(r *http.Request, app *models.Application) (clientIP string) {
	switch app.ClientIPMethod {
	case models.IPMethod_REMOTE_ADDR:
		clientIP, _, _ = net.SplitHostPort(r.RemoteAddr)
		return clientIP
	case models.IPMethod_X_FORWARDED_FOR:
		xForwardedFor := r.Header.Get("X-Forwarded-For")
		ips := strings.Split(xForwardedFor, ", ")
		clientIP = ips[len(ips)-1]
	case models.IPMethod_X_REAL_IP:
		clientIP = r.Header.Get("X-Real-IP")
	case models.IPMethod_REAL_IP:
		clientIP = r.Header.Get("Real-IP")
	}
	if len(clientIP) == 0 {
		clientIP, _, _ = net.SplitHostPort(r.RemoteAddr)
	}
	return clientIP
}

func OAuthLogout(w http.ResponseWriter, r *http.Request) {
	session, _ := store.Get(r, "janusec-token")
	session.Options = &sessions.Options{Path: "/", MaxAge: -1}
	session.Save(r, w)
	http.Redirect(w, r, "/", http.StatusTemporaryRedirect)
}
