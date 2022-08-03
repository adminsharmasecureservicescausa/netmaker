package logic

import (
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/gravitl/netmaker/database"
	"github.com/gravitl/netmaker/logger"
	"github.com/gravitl/netmaker/models"
)

// CreateEgressGateway - creates an egress gateway
func CreateEgressGateway(gateway models.EgressGatewayRequest) (models.Node, error) {
	node, err := GetNodeByID(gateway.NodeID)
	if err != nil {
		return models.Node{}, err
	}
	if node.OS != "linux" && node.OS != "freebsd" { // add in darwin later
		return models.Node{}, errors.New(node.OS + " is unsupported for egress gateways")
	}
	if gateway.NatEnabled == "" {
		gateway.NatEnabled = "yes"
	}
	err = ValidateEgressGateway(gateway)
	if err != nil {
		return models.Node{}, err
	}
	node.IsEgressGateway = "yes"
	node.EgressGatewayRanges = gateway.Ranges
	node.EgressGatewayNatEnabled = gateway.NatEnabled
	postUpCmd := ""
	postDownCmd := ""
	if node.OS == "linux" {
		// nftables only supported on Linux
		if node.IsNFTablesPresent == "yes" {
			// assumes chains eg FORWARD and POSTROUTING already exist
			logger.Log(3, "creating egress gateway using nftables")
			postUpCmd = "nft add rule ip filter FORWARD iifname " + node.Interface + " counter accept ; "
			postUpCmd += "nft add rule ip filter FORWARD oifname " + node.Interface + " counter accept ; "
			postDownCmd = "nft delete rule ip filter FORWARD iifname " + node.Interface + " counter accept ; "
			postDownCmd += "nft delete rule ip filter FORWARD oifname " + node.Interface + " counter accept ; "

			if node.EgressGatewayNatEnabled == "yes" {
				postUpCmd += "nft add rule ip nat POSTROUTING oifname " + node.Interface + " counter masquerade ;"
				postDownCmd += "nft delete rule ip nat POSTROUTING oifname " + node.Interface + " counter masquerade ;"
			}
		} else {
			logger.Log(3, "creating egress gateway using iptables")
			postUpCmd = "iptables -A FORWARD -i " + node.Interface + " -j ACCEPT; "
			postUpCmd += "iptables -A FORWARD -o " + node.Interface + " -j ACCEPT"
			postDownCmd = "iptables -D FORWARD -i " + node.Interface + " -j ACCEPT; "
			postDownCmd += "iptables -D FORWARD -o " + node.Interface + " -j ACCEPT"

			if node.EgressGatewayNatEnabled == "yes" {
				postUpCmd += "; iptables -t nat -A POSTROUTING -o " + gateway.Interface + " -j MASQUERADE"
				postDownCmd += "; iptables -t nat -D POSTROUTING -o " + gateway.Interface + " -j MASQUERADE"
			}
		}
	}
	if node.OS == "freebsd" {
		postUpCmd = "kldload ipfw ipfw_nat ; "
		postUpCmd += "ipfw disable one_pass ; "
		postUpCmd += "ipfw nat 1 config if " + gateway.Interface + " same_ports unreg_only reset ; "
		postUpCmd += "ipfw add 64000 reass all from any to any in ; "
		postUpCmd += "ipfw add 64000 nat 1 ip from any to any in via " + gateway.Interface + " ; "
		postUpCmd += "ipfw add 64000 check-state ; "
		postUpCmd += "ipfw add 64000 nat 1 ip from any to any out via " + gateway.Interface + " ; "
		postUpCmd += "ipfw add 65534 allow ip from any to any ; "
		postDownCmd = "ipfw delete 64000 ; "
		postDownCmd += "ipfw delete 65534 ; "
		postDownCmd += "kldunload ipfw_nat ipfw"

	}
	if gateway.PostUp != "" {
		postUpCmd = gateway.PostUp
	}
	if gateway.PostDown != "" {
		postDownCmd = gateway.PostDown
	}
	if node.PostUp != "" {
		if !strings.Contains(node.PostUp, postUpCmd) {
			postUpCmd = node.PostUp + "; " + postUpCmd
		}
	}
	if node.PostDown != "" {
		if !strings.Contains(node.PostDown, postDownCmd) {
			postDownCmd = node.PostDown + "; " + postDownCmd
		}
	}

	node.PostUp = postUpCmd
	node.PostDown = postDownCmd
	node.SetLastModified()
	nodeData, err := json.Marshal(&node)
	if err != nil {
		return node, err
	}
	if err = database.Insert(node.ID, string(nodeData), database.NODES_TABLE_NAME); err != nil {
		return models.Node{}, err
	}
	if err = NetworkNodesUpdatePullChanges(node.Network); err != nil {
		return models.Node{}, err
	}
	return node, nil
}

// ValidateEgressGateway - validates the egress gateway model
func ValidateEgressGateway(gateway models.EgressGatewayRequest) error {
	var err error

	empty := len(gateway.Ranges) == 0
	if empty {
		err = errors.New("IP Ranges Cannot Be Empty")
	}
	empty = gateway.Interface == ""
	if empty {
		err = errors.New("interface cannot be empty")
	}
	return err
}

// DeleteEgressGateway - deletes egress from node
func DeleteEgressGateway(network, nodeid string) (models.Node, error) {

	node, err := GetNodeByID(nodeid)
	if err != nil {
		return models.Node{}, err
	}

	node.IsEgressGateway = "no"
	node.EgressGatewayRanges = []string{}
	node.PostUp = ""
	node.PostDown = ""
	if node.IsIngressGateway == "yes" { // check if node is still an ingress gateway before completely deleting postdown/up rules
		// still have an ingress gateway so preserve it
		if node.OS == "linux" {
			// nftables only supported on Linux
			if node.IsNFTablesPresent == "yes" {
				// assumes chains eg FORWARD and POSTROUTING already exist
				logger.Log(3, "deleting egress gateway using nftables")
				node.PostUp = "nft add rule ip filter FORWARD iifname " + node.Interface + " counter accept ; "
				node.PostUp += "nft add rule ip filter FORWARD oifname " + node.Interface + " counter accept ; "
				node.PostUp += "nft add rule ip nat POSTROUTING oifname " + node.Interface + " counter masquerade ; "
				node.PostDown = "nft delete rule ip filter FORWARD iifname " + node.Interface + " counter accept ;"
				node.PostDown += "nft delete rule ip filter FORWARD iifname " + node.Interface + " counter accept ;"
				node.PostDown += "nft delete rule ip nat POSTROUTING oifname " + node.Interface + " counter masquerade "
			} else {
				logger.Log(3, "deleting egress gateway using iptables")
				node.PostUp = "iptables -A FORWARD -i " + node.Interface + " -j ACCEPT ; "
				node.PostUp += "iptables -A FORWARD -o " + node.Interface + " -j ACCEPT ; "
				node.PostUp += "iptables -t nat -A POSTROUTING -o " + node.Interface + " -j MASQUERADE"
				node.PostDown = "iptables -D FORWARD -i " + node.Interface + " -j ACCEPT ; "
				node.PostDown += "iptables -D FORWARD -o " + node.Interface + " -j ACCEPT ; "
				node.PostDown += "iptables -t nat -D POSTROUTING -o " + node.Interface + " -j MASQUERADE"
			}
		}
		// no need to preserve ingress gateway on FreeBSD as ingress is not supported on that OS
	}
	node.SetLastModified()

	data, err := json.Marshal(&node)
	if err != nil {
		return models.Node{}, err
	}
	if err = database.Insert(node.ID, string(data), database.NODES_TABLE_NAME); err != nil {
		return models.Node{}, err
	}
	if err = NetworkNodesUpdatePullChanges(network); err != nil {
		return models.Node{}, err
	}
	return node, nil
}

// CreateIngressGateway - creates an ingress gateway
func CreateIngressGateway(netid string, nodeid string) (models.Node, error) {

	var postUpCmd, postDownCmd string
	node, err := GetNodeByID(nodeid)
	if node.OS != "linux" { // add in darwin later
		return models.Node{}, errors.New(node.OS + " is unsupported for ingress gateways")
	}

	if err != nil {
		return models.Node{}, err
	}

	network, err := GetParentNetwork(netid)
	if err != nil {
		return models.Node{}, err
	}
	node.IsIngressGateway = "yes"
	node.IngressGatewayRange = network.AddressRange
	if node.IsNFTablesPresent == "yes" {
		// assumes chains eg FORWARD and POSTROUTING already exist
		logger.Log(3, "creating ingress gateway using nftables")
		postUpCmd = "nft add rule ip filter FORWARD iifname " + node.Interface + " counter accept ; "
		postUpCmd += "nft add rule ip filter FORWARD oifname " + node.Interface + " counter accept ; "
		postUpCmd += "nft add rule ip nat POSTROUTING oifname " + node.Interface + " counter masquerade"
		postDownCmd = "nft delete rule ip filter FORWARD iifname " + node.Interface + " counter accept ; "
		postDownCmd += "nft delete rule ip filter FORWARD oifname " + node.Interface + " counter accept ; "
		postDownCmd += "nft delete rule ip nat POSTROUTING oifname " + node.Interface + " counter masquerade"
	} else {
		logger.Log(3, "creating ingress gateway using iptables")
		postUpCmd = "iptables -A FORWARD -i " + node.Interface + " -j ACCEPT ; "
		postUpCmd += "iptables -A FORWARD -o " + node.Interface + " -j ACCEPT ; "
		postUpCmd += "iptables -t nat -A POSTROUTING -o " + node.Interface + " -j MASQUERADE"
		postDownCmd = "iptables -D FORWARD -i " + node.Interface + " -j ACCEPT ; "
		postDownCmd += "iptables -D FORWARD -o " + node.Interface + " -j ACCEPT ; "
		postDownCmd += "iptables -t nat -D POSTROUTING -o " + node.Interface + " -j MASQUERADE"
	}

	if node.PostUp != "" {
		if !strings.Contains(node.PostUp, postUpCmd) {
			postUpCmd = node.PostUp + "; " + postUpCmd
		}
	}
	if node.PostDown != "" {
		if !strings.Contains(node.PostDown, postDownCmd) {
			postDownCmd = node.PostDown + "; " + postDownCmd
		}
	}
	node.SetLastModified()
	node.PostUp = postUpCmd
	node.PostDown = postDownCmd
	node.UDPHolePunch = "no"

	data, err := json.Marshal(&node)
	if err != nil {
		return models.Node{}, err
	}
	err = database.Insert(node.ID, string(data), database.NODES_TABLE_NAME)
	if err != nil {
		return models.Node{}, err
	}
	err = SetNetworkNodesLastModified(netid)
	return node, err
}

// DeleteIngressGateway - deletes an ingress gateway
func DeleteIngressGateway(networkName string, nodeid string) (models.Node, error) {

	node, err := GetNodeByID(nodeid)
	if err != nil {
		return models.Node{}, err
	}
	network, err := GetParentNetwork(networkName)
	if err != nil {
		return models.Node{}, err
	}
	// delete ext clients belonging to ingress gateway
	if err = DeleteGatewayExtClients(node.ID, networkName); err != nil {
		return models.Node{}, err
	}
	logger.Log(3, "deleting ingress gateway")

	node.UDPHolePunch = network.DefaultUDPHolePunch
	node.LastModified = time.Now().Unix()
	node.IsIngressGateway = "no"
	node.IngressGatewayRange = ""

	if node.IsEgressGateway == "yes" { // check if node is still an egress gateway before completely deleting postdown/up rules
		// still have an egress gateway so preserve it
		if node.OS == "linux" {
			// nftables only supported on Linux
			if node.IsNFTablesPresent == "yes" {
				// preserve egress gateway via the setup that createegressgateway used
				// assumes chains eg FORWARD and POSTROUTING already exist
				logger.Log(3, "deleting ingress gateway: nftables in use")
				node.PostUp = "nft add rule ip filter FORWARD iifname " + node.Interface + " counter accept ; "
				node.PostUp += "nft add rule ip filter FORWARD oifname " + node.Interface + " counter accept ; "
				node.PostDown = "nft delete rule ip filter FORWARD iifname " + node.Interface + " counter accept ; "
				node.PostDown += "nft delete rule ip filter FORWARD oifname " + node.Interface + " counter accept ; "

				if node.EgressGatewayNatEnabled == "yes" {
					node.PostUp += "nft add rule ip nat POSTROUTING oifname " + node.Interface + " counter masquerade ;"
					node.PostDown += "nft delete rule ip nat POSTROUTING oifname " + node.Interface + " counter masquerade ;"
				}
			} else {
				// preserve egress gateway via the setup that createegressgateway used
				logger.Log(3, "deleting ingress gateway: iptables in use")
				node.PostUp = "iptables -A FORWARD -i " + node.Interface + " -j ACCEPT; "
				node.PostUp += "iptables -A FORWARD -o " + node.Interface + " -j ACCEPT"
				node.PostDown = "iptables -D FORWARD -i " + node.Interface + " -j ACCEPT; "
				node.PostDown += "iptables -D FORWARD -o " + node.Interface + " -j ACCEPT"

				if node.EgressGatewayNatEnabled == "yes" {
					node.PostUp += "; iptables -t nat -A POSTROUTING -o " + node.Interface + " -j MASQUERADE"
					node.PostDown += "; iptables -t nat -D POSTROUTING -o " + node.Interface + " -j MASQUERADE"
				}
			}
		}
		// preserve egress gateway via the setup that createegressgateway used
		if node.OS == "freebsd" {
			node.PostUp = "kldload ipfw ipfw_nat ; "
			node.PostUp += "ipfw disable one_pass ; "
			node.PostUp += "ipfw nat 1 config if " + node.Interface + " same_ports unreg_only reset ; "
			node.PostUp += "ipfw add 64000 reass all from any to any in ; "
			node.PostUp += "ipfw add 64000 nat 1 ip from any to any in via " + node.Interface + " ; "
			node.PostUp += "ipfw add 64000 check-state ; "
			node.PostUp += "ipfw add 64000 nat 1 ip from any to any out via " + node.Interface + " ; "
			node.PostUp += "ipfw add 65534 allow ip from any to any ; "
			node.PostDown = "ipfw delete 64000 ; "
			node.PostDown += "ipfw delete 65534 ; "
			node.PostDown += "kldunload ipfw_nat ipfw"
		}
	}

	data, err := json.Marshal(&node)
	if err != nil {
		return models.Node{}, err
	}
	err = database.Insert(node.ID, string(data), database.NODES_TABLE_NAME)
	if err != nil {
		return models.Node{}, err
	}
	err = SetNetworkNodesLastModified(networkName)
	return node, err
}

// DeleteGatewayExtClients - deletes ext clients based on gateway (mac) of ingress node and network
func DeleteGatewayExtClients(gatewayID string, networkName string) error {
	currentExtClients, err := GetNetworkExtClients(networkName)
	if err != nil && !database.IsEmptyRecord(err) {
		return err
	}
	for _, extClient := range currentExtClients {
		if extClient.IngressGatewayID == gatewayID {
			if err = DeleteExtClient(networkName, extClient.ClientID); err != nil {
				logger.Log(1, "failed to remove ext client", extClient.ClientID)
				continue
			}
		}
	}
	return nil
}
