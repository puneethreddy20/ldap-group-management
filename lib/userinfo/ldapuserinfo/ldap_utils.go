package ldapuserinfo

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"github.com/Symantec/keymaster/lib/authutil"
	"github.com/Symantec/ldap-group-management/lib/userinfo"
	"gopkg.in/ldap.v2"
	"log"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const ldapTimeoutSecs = 10

type UserInfoLDAPSource struct {
	BindUsername          string `yaml:"bind_username"`
	BindPassword          string `yaml:"bind_password"`
	LDAPTargetURLs        string `yaml:"ldap_target_urls"`
	UserSearchBaseDNs     string `yaml:"user_search_base_dns"`
	UserSearchFilter      string `yaml:"user_search_filter"`
	GroupSearchBaseDNs    string `yaml:"group_search_base_dns"`
	GroupSearchFilter     string `yaml:"group_search_filter"`
	Admins                string `yaml:"super_admins"`
	ServiceAccountBaseDNs string `yaml:"service_search_base_dns"`
}

func getLDAPConnection(u url.URL, timeoutSecs uint, rootCAs *x509.CertPool) (*ldap.Conn, string, error) {

	if u.Scheme != "ldaps" {
		err := errors.New("Invalid ldaputil scheme (we only support ldaps)")
		log.Println(err)
		return nil, "", err
	}

	serverPort := strings.Split(u.Host, ":")
	port := "636"

	if len(serverPort) == 2 {
		port = serverPort[1]
	}

	server := serverPort[0]
	hostnamePort := server + ":" + port

	timeout := time.Duration(time.Duration(timeoutSecs) * time.Second)
	start := time.Now()

	tlsConn, err := tls.DialWithDialer(&net.Dialer{Timeout: timeout}, "tcp",
		hostnamePort, &tls.Config{ServerName: server, RootCAs: rootCAs})

	if err != nil {
		log.Printf("rooCAs=%+v,  serverName=%s, hostnameport=%s, tlsConn=%+v", rootCAs, server, hostnamePort, tlsConn)
		errorTime := time.Since(start).Seconds() * 1000
		log.Printf("connection failure for:%s (%s)(time(ms)=%v)", server, err.Error(), errorTime)
		return nil, "", err
	}

	// we dont close the tls connection directly  close defer to the new ldaputil connection
	conn := ldap.NewConn(tlsConn, true)
	return conn, server, nil
}

func (u *UserInfoLDAPSource) getTargetLDAPConnection() (*ldap.Conn, error) {
	var ldapURL []*url.URL
	for _, ldapURLString := range strings.Split(u.LDAPTargetURLs, ",") {
		newURL, err := authutil.ParseLDAPURL(ldapURLString)
		if err != nil {
			log.Println(err)
			continue
		}
		ldapURL = append(ldapURL, newURL)
	}

	for _, TargetLdapUrl := range ldapURL {
		conn, _, err := getLDAPConnection(*TargetLdapUrl, ldapTimeoutSecs, nil)

		if err != nil {
			log.Println(err)
			continue
		}
		timeout := time.Duration(time.Duration(ldapTimeoutSecs) * time.Second)
		conn.SetTimeout(timeout)
		conn.Start()

		err = conn.Bind(u.BindUsername, u.BindPassword)
		if err != nil {
			log.Println(err)
			continue
		}
		return conn, nil
	}
	return nil, errors.New("cannot connect to LDAP server")
}

//Get all ldaputil users and put that in map ---required
func (u *UserInfoLDAPSource) GetallUsers() (map[string]string, error) {

	conn, err := u.getTargetLDAPConnection()
	if err != nil {
		log.Println(err)
		return nil, err
	}
	defer conn.Close()

	AllUsersinLdap := make(map[string]string)

	Attributes := []string{"uid"}
	searchrequest := ldap.NewSearchRequest(u.UserSearchBaseDNs, ldap.ScopeWholeSubtree,
		ldap.NeverDerefAliases, 0, 0, false, u.UserSearchFilter, Attributes, nil)
	result, err := conn.Search(searchrequest)
	if err != nil {
		log.Println(err)
		return nil, err
	}

	if len(result.Entries) == 0 {
		log.Println("No records found")
		return nil, errors.New("No records found")
	}
	for _, entry := range result.Entries {
		uid := entry.GetAttributeValue("uid")
		AllUsersinLdap[uid] = uid
	}

	return AllUsersinLdap, nil
}

//To build a user base DN using uid only for Target LDAP.
func (u *UserInfoLDAPSource) CreateuserDn(username string) string {
	//uid := username
	result := "uid=" + username + "," + u.UserSearchBaseDNs

	return string(result)

}

//To build a GroupDN for a particular group in Target ldaputil
func (u *UserInfoLDAPSource) CreategroupDn(groupname string) string {
	result := "cn=" + groupname + "," + u.GroupSearchBaseDNs

	return string(result)

}

func (u *UserInfoLDAPSource) CreateserviceDn(groupname string) string {
	result := "cn=" + groupname + "," + u.ServiceAccountBaseDNs

	return string(result)
}

//Creating a Group --required
func (u *UserInfoLDAPSource) CreateGroup(groupinfo userinfo.GroupInfo) error {
	conn, err := u.getTargetLDAPConnection()
	if err != nil {
		log.Println(err)
		return err
	}
	defer conn.Close()

	entry := u.CreategroupDn(groupinfo.Groupname)
	gidnum, err := u.GetmaximumGidnumber()
	if err != nil {
		log.Println(err)
		return err
	}
	group := ldap.NewAddRequest(entry)
	group.Attribute("objectClass", []string{"posixGroup", "top", "groupOfNames"})
	group.Attribute("cn", []string{groupinfo.Groupname})
	group.Attribute("description", []string{groupinfo.Description})
	group.Attribute("member", groupinfo.Member)
	group.Attribute("memberUid", groupinfo.MemberUid)
	group.Attribute("gidNumber", []string{gidnum})
	err = conn.Add(group)
	if err != nil {
		log.Println(err)
		return err
	}
	return nil
}

//deleting a Group from target ldaputil. --required
func (u *UserInfoLDAPSource) DeleteGroup(groupnames []string) error {
	conn, err := u.getTargetLDAPConnection()
	if err != nil {
		log.Println(err)
		return err
	}
	defer conn.Close()

	for _, entry := range groupnames {
		groupdn := u.CreategroupDn(entry)

		DelReq := ldap.NewDelRequest(groupdn, nil)
		err := conn.Del(DelReq)
		if err != nil {
			log.Println(err)
			return err
		}

	}
	return nil
}

//Adding an attritube called 'description' to a dn in Target Ldap --required
func (u *UserInfoLDAPSource) AddAtributedescription(groupname string) error {
	conn, err := u.getTargetLDAPConnection()
	if err != nil {
		log.Println(err)
		return err
	}
	defer conn.Close()

	entry := u.CreategroupDn(groupname)
	modify := ldap.NewModifyRequest(entry)
	modify.Delete("description", []string{"self-managed"})

	//modify.Add("description", []string{"created by me"})
	err = conn.Modify(modify)
	if err != nil {
		log.Println(err)
		return err
	}
	return nil

}

//Deleting the attribute in a dn in Target Ldap. --required
func (u *UserInfoLDAPSource) DeleteDescription(groupnames []string) error {

	conn, err := u.getTargetLDAPConnection()
	if err != nil {
		log.Println(err)
		return err
	}
	defer conn.Close()

	for _, entry := range groupnames {
		entry = u.CreategroupDn(entry)

		modify := ldap.NewModifyRequest(entry)

		modify.Delete("description", []string{"created by Midpoint"})
		err := conn.Modify(modify)
		if err != nil {
			log.Println(err)
			return err
		}
	}
	return nil
}

//function to get all the groups in Target ldaputil and put it in array --required
func (u *UserInfoLDAPSource) GetallGroups() ([]string, error) {
	conn, err := u.getTargetLDAPConnection()
	if err != nil {
		log.Println(err)
		return nil, err
	}
	defer conn.Close()

	var AllGroups []string
	searchrequest := ldap.NewSearchRequest(u.GroupSearchBaseDNs, ldap.ScopeWholeSubtree, ldap.NeverDerefAliases,
		0, 0, false, u.GroupSearchFilter, []string{"cn"}, nil)
	result, err := conn.Search(searchrequest)
	if err != nil {
		log.Println(err)
		return nil, err
	}
	for _, entry := range result.Entries {
		AllGroups = append(AllGroups, entry.GetAttributeValue("cn"))
	}
	return AllGroups, nil
}

// GetGroupsOfUser returns the all groups of a user. --required
func (u *UserInfoLDAPSource) GetgroupsofUser(username string) ([]string, error) {
	conn, err := u.getTargetLDAPConnection()
	if err != nil {
		log.Println(err)
		return nil, err
	}
	defer conn.Close()

	searchRequest := ldap.NewSearchRequest(
		u.GroupSearchBaseDNs,
		ldap.ScopeWholeSubtree, ldap.NeverDerefAliases, 0, 0, false,
		"(&(memberUid="+username+" ))",
		[]string{"cn"}, //memberOf (if searching other way around using usersdn instead of groupdn)
		nil,
	)
	sr, err := conn.Search(searchRequest)
	if err != nil {
		log.Println(err)
		return nil, err
	}
	groups := []string{}
	for _, entry := range sr.Entries {
		groups = append(groups, entry.GetAttributeValue("cn"))
	}
	return groups, nil
}

//returns all the users of a group --required
func (u *UserInfoLDAPSource) GetusersofaGroup(groupname string) ([]string, string, error) {
	conn, err := u.getTargetLDAPConnection()
	if err != nil {
		log.Println(err)
		return nil, "", err
	}
	defer conn.Close()
	//Base := u.CreategroupDn(groupname)

	searchRequest := ldap.NewSearchRequest(
		u.GroupSearchBaseDNs,
		ldap.ScopeWholeSubtree, ldap.NeverDerefAliases, 0, 0, false,
		"(&(cn="+groupname+" )(objectClass=posixGroup))",
		nil,
		nil,
	)
	sr, err := conn.Search(searchRequest)
	if err != nil {
		log.Println(err)
		return nil, "", err
	}
	if len(sr.Entries) > 1 {
		log.Println("Duplicate entries found")
		return nil, "", errors.New("Multiple entries found, Contact the administrator!")
	}
	users := sr.Entries[0].GetAttributeValues("memberUid")
	description := sr.Entries[0].GetAttributeValue("description")
	return users, description, nil
}

//parse super admins of Target Ldap
func (u *UserInfoLDAPSource) ParseSuperadmins() []string {
	var superAdminsInfo []string
	for _, admin := range strings.Split(u.Admins, ",") {
		superAdminsInfo = append(superAdminsInfo, admin)
	}
	return superAdminsInfo
}

//if user is super admin or not
func (u *UserInfoLDAPSource) UserisadminOrNot(username string) bool {
	superAdmins := u.ParseSuperadmins()
	for _, user := range superAdmins {
		if user == username {
			return true
		}
	}
	return false
}

//it helps to findout the current maximum gid number in ldaputil.
func (u *UserInfoLDAPSource) GetmaximumGidnumber() (string, error) {
	conn, err := u.getTargetLDAPConnection()
	if err != nil {
		log.Println(err)
		return "error in getTargetLDAPConnection", err
	}
	defer conn.Close()
	searchRequest := ldap.NewSearchRequest(
		u.GroupSearchBaseDNs,
		ldap.ScopeWholeSubtree, ldap.NeverDerefAliases, 0, 0, false,
		"(|(objectClass=posixGroup)(objectClass=groupOfNames))",
		[]string{"gidNumber"},
		nil,
	)
	sr, err := conn.Search(searchRequest)
	if err != nil {
		log.Println(err)
		return "error in ldapsearch", err
	}
	var max = 0
	for _, entry := range sr.Entries {
		gidnum := entry.GetAttributeValue("gidNumber")
		value, _ := strconv.Atoi(gidnum)
		//if err!=nil{
		//	log.Println(err)
		//}
		if value > max {
			max = value
		}
	}
	//fmt.Println(max)
	return fmt.Sprint(max + 1), nil
}

//adding members to existing group
func (u *UserInfoLDAPSource) AddmemberstoExisting(groupinfo userinfo.GroupInfo) error {
	conn, err := u.getTargetLDAPConnection()
	if err != nil {
		log.Println(err)
		return err
	}
	defer conn.Close()
	entry := u.CreategroupDn(groupinfo.Groupname)
	modify := ldap.NewModifyRequest(entry)
	modify.Add("memberUid", groupinfo.MemberUid)
	modify.Add("member", groupinfo.Member)
	err = conn.Modify(modify)
	if err != nil {
		log.Println(err)
		return err
	}
	return nil
}

//remove members from existing group
func (u *UserInfoLDAPSource) DeletemembersfromGroup(groupinfo userinfo.GroupInfo) error {
	conn, err := u.getTargetLDAPConnection()
	if err != nil {
		log.Println(err)
		return err
	}
	defer conn.Close()
	entry, err := u.GetGroupDN(groupinfo.Groupname)
	if err != nil {
		log.Println(err)
		return err
	}
	modify := ldap.NewModifyRequest(entry)
	modify.Delete("memberUid", groupinfo.MemberUid)
	modify.Delete("member", groupinfo.Member)
	err = conn.Modify(modify)
	if err != nil {
		log.Println(err)
		return err
	}
	return nil
}

//if user is already a member of group or not
func (u *UserInfoLDAPSource) IsgroupmemberorNot(groupname string, username string) (bool, string, error) {

	AllUsersinGroup, description, err := u.GetusersofaGroup(groupname)
	if err != nil {
		log.Println(err)
		return false, "", err
	}
	for _, entry := range AllUsersinGroup {
		if entry == username {
			return true, description, nil
		}
	}
	return false, description, nil
}

//get description of a group
func (u *UserInfoLDAPSource) GetDescriptionvalue(groupname string) (string, error) {
	conn, err := u.getTargetLDAPConnection()
	if err != nil {
		log.Println(err)
		return "Error in getTargetLDAPConnection", err
	}
	defer conn.Close()

	Base, err := u.GetGroupDN(groupname)
	if err != nil {
		log.Println(err)
		return "", err
	}

	searchRequest := ldap.NewSearchRequest(
		Base,
		ldap.ScopeWholeSubtree, ldap.NeverDerefAliases, 0, 0, false,
		u.GroupSearchFilter,
		[]string{"description"},
		nil,
	)
	sr, err := conn.Search(searchRequest)
	if err != nil {
		log.Println(err)
		return "", err
	}
	if len(sr.Entries) > 1 {
		log.Println("Duplicate entries found")
		return "", errors.New("Multiple entries found, Contact the administrator!")
	}
	descriptionValue := sr.Entries[0].GetAttributeValue("description")

	return descriptionValue, nil
}

//get email of a user
func (u *UserInfoLDAPSource) GetEmailofauser(username string) ([]string, error) {
	var userEmail []string
	conn, err := u.getTargetLDAPConnection()
	if err != nil {
		log.Println(err)
		return nil, err
	}
	defer conn.Close()
	Userdn := u.CreateuserDn(username)
	searchrequest := ldap.NewSearchRequest(Userdn, ldap.ScopeWholeSubtree, ldap.NeverDerefAliases,
		0, 0, false, "((objectClass=*))", []string{"mail"}, nil)
	result, err := conn.Search(searchrequest)
	if err != nil {
		log.Println(err)
		return nil, err
	}
	userEmail = append(userEmail, result.Entries[0].GetAttributeValues("mail")[0])
	return userEmail, nil

}

//get email of all users in the given group
func (u *UserInfoLDAPSource) GetEmailofusersingroup(groupname string) ([]string, error) {

	groupUsers, _, err := u.GetusersofaGroup(groupname)
	if err != nil {
		log.Println(err)
		return nil, err
	}
	var userEmail []string
	for _, entry := range groupUsers {
		value, err := u.GetEmailofauser(entry)
		if err != nil {
			log.Println(err)
			return nil, err
		}
		userEmail = append(userEmail, value[0])

	}
	return userEmail, nil
}

func (u *UserInfoLDAPSource) CreateServiceAccount(groupinfo userinfo.GroupInfo) error {
	conn, err := u.getTargetLDAPConnection()
	if err != nil {
		log.Println(err)
		return err
	}
	defer conn.Close()

	entry := u.CreateserviceDn(groupinfo.Groupname)
	gidnum, err := u.GetmaximumGidnumber()
	if err != nil {
		log.Println(err)
		return err
	}
	group := ldap.NewAddRequest(entry)
	group.Attribute("objectClass", []string{"posixGroup", "top", "groupOfNames"})
	group.Attribute("cn", []string{groupinfo.Groupname})
	group.Attribute("description", []string{groupinfo.Description})
	group.Attribute("gidNumber", []string{gidnum})
	err = conn.Add(group)
	if err != nil {
		log.Println(err)
		return err
	}
	return nil
}

func (u *UserInfoLDAPSource) IsgroupAdminorNot(username string, groupname string) (bool, error) {
	conn, err := u.getTargetLDAPConnection()
	if err != nil {
		log.Println(err)
		return false, err
	}
	defer conn.Close()

	managedby, err := u.GetDescriptionvalue(groupname)
	if managedby == "self-managed" {
		Isgroupmember, _, err := u.IsgroupmemberorNot(groupname, username)
		if !Isgroupmember || err != nil {
			log.Println(err)
			return false, err
		}
		return true, nil
	}
	Isgroupmember, _, err := u.IsgroupmemberorNot(managedby, username)
	if !Isgroupmember || err != nil {
		log.Println(err)
		return false, err
	}

	return true, nil
}

func (u *UserInfoLDAPSource) UsernameExistsornot(username string) (bool, error) {
	conn, err := u.getTargetLDAPConnection()
	if err != nil {
		log.Println(err)
		return false, err
	}
	defer conn.Close()

	Attributes := []string{"uid"}
	searchrequest := ldap.NewSearchRequest(u.UserSearchBaseDNs, ldap.ScopeWholeSubtree,
		ldap.NeverDerefAliases, 0, 0, false, "(&(uid="+username+" ))", Attributes, nil)
	result, err := conn.Search(searchrequest)
	if err != nil {
		log.Println("Error in Ldap Search")
		return false, err
	}

	if len(result.Entries) == 0 {
		log.Println("No records found")
		return false, nil
	}
	if len(result.Entries) > 1 {
		log.Println("duplicate entries!")
		return false, errors.New("Multiple entries available! Contact the administration!")
	}
	if username == result.Entries[0].GetAttributeValue("uid") {
		return true, nil
	}

	return false, nil
}

func (u *UserInfoLDAPSource) GroupnameExistsornot(groupname string) (bool, string, error) {
	conn, err := u.getTargetLDAPConnection()
	if err != nil {
		log.Println(err)
		return false, "", err
	}
	defer conn.Close()

	Attributes := []string{"cn"}
	searchrequest := ldap.NewSearchRequest(u.GroupSearchBaseDNs, ldap.ScopeWholeSubtree,
		ldap.NeverDerefAliases, 0, 0, false, "(&(cn="+groupname+" )(objectClass=posixGroup))",
		Attributes, nil)

	result, err := conn.Search(searchrequest)
	if err != nil {
		log.Println("Error in ldap search")
		return false, "", err
	}

	if len(result.Entries) == 0 {
		log.Println("No records found")
		return false, "", nil
	}
	if len(result.Entries) > 1 {
		log.Println("duplicate entries!")
		return false, "", errors.New("Multiple entries available! Contact the administration!")
	}
	if groupname != result.Entries[0].GetAttributeValue("cn") {
		return false, "", nil
	}

	description := result.Entries[0].GetAttributeValue("description")

	return true, description, nil
}

func (u *UserInfoLDAPSource) ServiceAccountExistsornot(groupname string) (bool, error) {
	conn, err := u.getTargetLDAPConnection()
	if err != nil {
		log.Println(err)
		return false, err
	}
	defer conn.Close()

	Attributes := []string{"cn"}
	searchrequest := ldap.NewSearchRequest(u.ServiceAccountBaseDNs, ldap.ScopeWholeSubtree,
		ldap.NeverDerefAliases, 0, 0, false, "(&(cn="+groupname+" )(objectClass=posixGroup))",
		Attributes, nil)
	result, err := conn.Search(searchrequest)
	if err != nil {
		log.Println(err)
		return false, err
	}

	if len(result.Entries) == 0 {
		log.Println("No records found")
		return false, nil
	}
	if len(result.Entries) > 1 {
		log.Println("duplicate entries!")
		return false, errors.New("Multiple entries available! Contact the administration!")
	}
	if groupname == result.Entries[0].GetAttributeValue("cn") {
		return true, nil
	}

	return false, nil
}

func (u *UserInfoLDAPSource) GetGroupDN(groupname string) (string, error) {
	conn, err := u.getTargetLDAPConnection()
	if err != nil {
		log.Println(err)
		return "", err
	}
	defer conn.Close()

	searchRequest := ldap.NewSearchRequest(
		u.GroupSearchBaseDNs,
		ldap.ScopeWholeSubtree, ldap.NeverDerefAliases, 0, 0, false,
		"(&(cn="+groupname+" ))",
		nil,
		nil,
	)
	sr, err := conn.Search(searchRequest)
	if err != nil {
		log.Println(err)
		return "", err
	}
	if len(sr.Entries) > 1 {
		log.Println("Duplicate entries found")
		return "", errors.New("Multiple entries found, Contact the administrator!")
	}
	users := sr.Entries[0].DN
	return users, nil

}
