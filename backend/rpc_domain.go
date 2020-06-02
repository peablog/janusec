/*
 * @Copyright Reserved By Janusec (https://www.janusec.com/).
 * @Author: U2
 * @Date: 2018-07-14 16:23:23
 * @Last Modified: U2, 2018-07-14 16:23:23
 */

package backend

import (
	"encoding/json"

	"github.com/Janusec/janusec/data"
	"github.com/Janusec/janusec/models"
	"github.com/Janusec/janusec/utils"
)

func RPCSelectDomains() (dbDomains []*models.DBDomain) {
	rpcRequest := &models.RPCRequest{
		Action: "getdomains", Object: nil}
	resp, err := data.GetRPCResponse(rpcRequest)
	if err != nil {
		utils.CheckError("RPCSelectDomains GetResponse", err)
		return nil
	}
	rpcDBDomains := new(models.RPCDBDomains)
	err = json.Unmarshal(resp, rpcDBDomains)
	if err != nil {
		utils.CheckError("RPCSelectDomains Unmarshal", err)
		return nil
	}
	dbDomains = rpcDBDomains.Object
	return dbDomains
}
