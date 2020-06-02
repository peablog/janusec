/*
 * @Copyright Reserved By Janusec (https://www.janusec.com/).
 * @Author: U2
 * @Date: 2018-07-14 16:23:18
 * @Last Modified: U2, 2018-07-14 16:23:18
 */

package backend

import (
	"crypto/tls"
	"encoding/json"

	"github.com/Janusec/janusec/data"
	"github.com/Janusec/janusec/models"
	"github.com/Janusec/janusec/utils"
)

func RPCSelectCertificates() (certs []*models.CertItem) {
	rpcRequest := &models.RPCRequest{
		Action: "getcerts", Object: nil}
	resp, err := data.GetRPCResponse(rpcRequest)
	if err != nil {
		utils.CheckError("RPCSelectCertificates GetResponse", err)
		return nil
	}
	rpcCertItems := new(models.RPCCertItems)
	if err = json.Unmarshal(resp, rpcCertItems); err != nil {
		utils.CheckError("RPCSelectCertificates Unmarshal", err)
		return nil
	}
	certItems := rpcCertItems.Object
	for _, certItem := range certItems {
		certItem.TlsCert, err = tls.X509KeyPair([]byte(certItem.CertContent), []byte(certItem.PrivKeyContent))
		utils.CheckError("RPCSelectCertificates X509KeyPair", err)
		certs = append(certs, certItem)
	}
	return certs
}
