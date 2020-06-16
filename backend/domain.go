/*
 * @Copyright Reserved By Janusec (https://www.janusec.com/).
 * @Author: U2
 * @Date: 2018-07-14 16:22:10
 * @Last Modified: U2, 2018-07-14 16:22:10
 */

package backend

import (
	"fmt"
	"github.com/Janusec/janusec/utils"
	"io"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"github.com/Janusec/janusec/data"
	"github.com/Janusec/janusec/models"
)

var (
	Domains    []*models.Domain
	DomainsMap sync.Map //DomainsMap (string, models.DomainRelation)
)

func LoadDomains() {
	Domains = Domains[0:0]
	DomainsMap.Range(func(key, value interface{}) bool {
		DomainsMap.Delete(key)
		return true
	})
	var dbDomains []*models.DBDomain
	if data.IsMaster {
		dbDomains = data.DAL.SelectDomains()
	} else {
		dbDomains = RPCSelectDomains()
	}
	for _, dbDomain := range dbDomains {
		pApp, _ := GetApplicationByID(dbDomain.AppID)
		pCert, _ := SysCallGetCertByID(dbDomain.CertID)
		domain := &models.Domain{
			ID:       dbDomain.ID,
			Name:     dbDomain.Name,
			AppID:    dbDomain.AppID,
			CertID:   dbDomain.CertID,
			Redirect: dbDomain.Redirect,
			Location: dbDomain.Location,
			App:      pApp,
			Cert:     pCert}
		Domains = append(Domains, domain)
		DomainsMap.Store(domain.Name, models.DomainRelation{App: pApp, Cert: pCert, Redirect: dbDomain.Redirect, Location: dbDomain.Location})
	}
}

func IsStaticDir(domain string, path string) bool {
	if strings.Contains(path, "?") {
		return false
	}
	vv, _ := DomainsMap.Load(domain)
	app := vv.(models.DomainRelation).App
	fileExts := [8]string{".js", ".css", ".png", ".svg", ".jpg", ".jpeg", ".ttf", ".otf"}
	for _, ext := range fileExts {
		if strings.HasSuffix(path, ext) {
			localStaticFile := "./cdn_static_files/" + strconv.Itoa(int(app.ID)) + path
			if _, err := os.Stat(localStaticFile); os.IsNotExist(err) {
				targetUrl := app.InternalScheme + "://" + app.Destinations[0].Destination + path
				fmt.Println("FileNotExist try get from origin site:", targetUrl)
				req, err := http.NewRequest("GET", targetUrl, nil)
				utils.CheckError("new cache request error", err)
				// 源站地址
				var desAddr string
				if strings.Contains(app.Destinations[0].Destination, ":") {
					desAddr = strings.Split(app.Destinations[0].Destination, ":")[0]
				} else {
					desAddr = app.Destinations[0].Destination
				}
				isDomain, _ := regexp.MatchString("[a-zA-Z]", desAddr)
				// 如果源站配置的域名则使用域名回源，否则使用IP
				if isDomain {
					req.Host = desAddr
				} else {
					req.Host = domain
				}
				client := &http.Client{}
				resp, err := client.Do(req)
				utils.CheckError("do cache request error", err)
				defer resp.Body.Close()
				if resp.StatusCode != 200 {
					fmt.Println("should cache but StatusCode mismatch: ", resp.StatusCode)
					return false
				}
				if len(resp.Header["Content-Type"]) > 0 && !utils.Contains([]string{"image/png", "image/jpeg", "image/svg+xml", "text/css", "text/css; charset=utf-8", "application/javascript", "binary/octet-stream", "application/octet-stream"}, resp.Header["Content-Type"][0]) {
					fmt.Println("should cache but content-type mismatch: ", resp.Header["Content-Type"][0])
					return false
				}
				pathAll := utils.GetDirAll(localStaticFile)
				err = os.MkdirAll(pathAll, 0777)
				utils.CheckError("create cache dir error", err)
				f, err := os.Create(localStaticFile)
				utils.CheckError("do cache file error", err)
				size, err := io.Copy(f, resp.Body)
				utils.CheckError("write cache file error", err)
				fmt.Println("CDN Copy:", targetUrl, size)
			}
			return true
		}
	}
	return false
}

func GetDomainByID(id int64) *models.Domain {
	for _, domain := range Domains {
		if domain.ID == id {
			return domain
		}
	}
	return nil
}

func GetDomainByName(domain_name string) *models.Domain {
	for _, domain := range Domains {
		if domain.Name == domain_name {
			return domain
		}
	}
	return nil
}

func UpdateDomain(app *models.Application, domainMapInterface interface{}) *models.Domain {
	var domainMap = domainMapInterface.(map[string]interface{})
	domainID := int64(domainMap["id"].(float64))
	domainName := domainMap["name"].(string)
	certID := int64(domainMap["cert_id"].(float64))
	redirect := domainMap["redirect"].(bool)
	location := domainMap["location"].(string)
	pCert, _ := SysCallGetCertByID(certID)
	domain := GetDomainByID(domainID)
	if domainID == 0 {
		// New domain
		newDomainID := data.DAL.InsertDomain(domainName, app.ID, certID, redirect, location)
		domain = new(models.Domain)
		domain.ID = newDomainID
		Domains = append(Domains, domain)
	} else {
		data.DAL.UpdateDomain(domainName, app.ID, certID, redirect, location, domain.ID)
	}
	domain.Name = domainName
	domain.AppID = app.ID
	domain.CertID = certID
	domain.Redirect = redirect
	domain.Location = location
	domain.App = app
	domain.Cert = pCert
	DomainsMap.Store(domainName, models.DomainRelation{App: app, Cert: pCert, Redirect: redirect, Location: location})
	return domain
}

func GetDomainIndex(domain *models.Domain) int {
	for i := 0; i < len(Domains); i++ {
		if Domains[i].ID == domain.ID {
			return i
		}
	}
	return -1
}

func DeleteDomain(domain *models.Domain) {
	i := GetDomainIndex(domain)
	//fmt.Println("DeleteDomain Domains", Domains)
	//fmt.Println("DeleteDomain i=", i)
	Domains = append(Domains[:i], Domains[i+1:]...)
}

func DeleteDomainsByApp(app *models.Application) {
	for _, domain := range app.Domains {
		DeleteDomain(domain)
		//delete(DomainsMap, domain.Name)
		DomainsMap.Delete(domain.Name)
	}
	data.DAL.DeleteDomainByAppID(app.ID)
	/*
	   _,err := DB.Exec("DELETE FROM domains where app_id=$1",app.ID)
	   utils.CheckError(err)
	*/
}

func InterfaceContainsDomainID(domains []interface{}, domain_id int64) bool {
	for _, domain := range domains {
		destMap := domain.(map[string]interface{})
		id := int64(destMap["id"].(float64))
		if id == domain_id {
			return true
		}
	}
	return false
}
