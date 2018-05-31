package main

import (
	"database/sql"
	"errors"
	"flag"
	"github.com/Symantec/ldap-group-management/lib/userinfo"
	"github.com/Symantec/ldap-group-management/lib/userinfo/ldapuserinfo"
	"github.com/cviecco/go-simple-oidc-auth/authhandler"
	"gopkg.in/yaml.v2"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"sync"
	"time"
)

type baseConfig struct {
	HttpAddress           string `yaml:"http_address"`
	TLSCertFilename       string `yaml:"tls_cert_filename"`
	TLSKeyFilename        string `yaml:"tls_key_filename"`
	StorageURL            string `yaml:"storage_url"`
	OpenIDCConfigFilename string `yaml:"openidc_config_filename"`
	SMTPserver            string `yaml:"smtp_server"`
	SmtpSenderAddress     string `yaml:"smtp_sender_address"`
}

type AppConfigFile struct {
	Base       baseConfig                      `yaml:"base"`
	SourceLDAP ldapuserinfo.UserInfoLDAPSource `yaml:"source_config"`
	TargetLDAP ldapuserinfo.UserInfoLDAPSource `yaml:"target_config"`
}

type RuntimeState struct {
	Config      AppConfigFile
	dbType      string
	db          *sql.DB
	Userinfo    userinfo.UserInfo
	authcookies map[string]cookieInfo
	cookiemutex sync.Mutex
}

type cookieInfo struct {
	Username  string
	ExpiresAt time.Time
}
type GetGroups struct {
	AllGroups []string `json:"allgroups"`
}

type GetUsers struct {
	Users []string `json:"Users"`
}

type GetUserGroups struct {
	UserName   string   `json:"Username"`
	UserGroups []string `json:"usergroups"`
}

type GetGroupUsers struct {
	GroupName  string   `json:"groupname"`
	Groupusers []string `json:"Groupusers"`
}

type Response struct {
	UserName            string
	Groups              [][]string
	Users               []string
	PendingActions      [][]string
	GroupName           string
	GroupManagedbyValue string
	GroupUsers          []string
}

var (
	configFilename = flag.String("config", "config.yml", "The filename of the configuration")
	//tpl *template.Template
	//debug          = flag.Bool("debug", false, "enable debugging output")
	authSource *authhandler.SimpleOIDCAuth
)

const (
	descriptionAttribute  = "self-managed"
	cookieExpirationHours = 12
	cookieName            = "smallpointauth"

	allgroupsPath               = "/allgroups"
	allusersPath                = "/allusers"
	usergroupsPath              = "/user_groups/"
	groupusersPath              = "/group_users/"
	creategroupWebPagePath      = "/create_group"
	deletegroupWebPagePath      = "/delete_group"
	creategroupPath             = "/create_group/"
	deletegroupPath             = "/delete_group/"
	requestaccessPath           = "/requestaccess"
	allLDAPgroupsPath           = "/allGroups"
	pendingactionsPath          = "/pending-actions"
	pendingrequestsPath         = "/pending-requests"
	deleterequestsPath          = "/deleterequests"
	exitgroupPath               = "/exitgroup"
	loginPath                   = "/login"
	approverequestPath          = "/approve-request"
	rejectrequestPath           = "/reject-request"
	addmembersbuttonPath        = "/addmembers/"
	addmembersPath              = "/addmembers"
	deletemembersPath           = "/deletemembers"
	deletemembersbuttonPath     = "/deletemembers/"
	createServiceAccWebPagePath = "/create_serviceaccount"
	createServiceAccountPath    = "/create_serviceaccount/"
	groupinfoPath               = "/group_info/"

	indexPath = "/"
	authPath  = "/auth/oidcsimple/callback"

	templatesdirectoryPath = "templates"
	cssPath                = "/css/"
	imagesPath             = "/images/"
	jsPath                 = "/js/"
)

//parses the config file
func loadConfig(configFilename string) (RuntimeState, error) {

	var state RuntimeState

	if _, err := os.Stat(configFilename); os.IsNotExist(err) {
		err = errors.New("mising config file failure")
		return state, err
	}

	//ioutil.ReadFile returns a byte slice (i.e)(source)
	source, err := ioutil.ReadFile(configFilename)
	if err != nil {
		err = errors.New("cannot read config file")
		return state, err
	}

	//Unmarshall(source []byte,out interface{})decodes the source byte slice/value and puts them in out.
	err = yaml.Unmarshal(source, &state.Config)

	if err != nil {
		err = errors.New("Cannot parse config file")
		log.Printf("Source=%s", source)
		return state, err
	}
	err = initDB(&state)
	if err != nil {
		return state, err
	}
	state.Userinfo = &state.Config.TargetLDAP
	state.authcookies = make(map[string]cookieInfo)
	return state, err
}

type mailAttributes struct {
	RequestedUser string
	OtherUser     string
	Groupname     string
	RemoteAddr    string
	Browser       string
	OS            string
}

func main() {
	flag.Parse()

	state, err := loadConfig(*configFilename)
	if err != nil {
		log.Println("Missing config file")
		panic(err)
	}
	var openidConfigFilename = state.Config.Base.OpenIDCConfigFilename //"/etc/openidc_config_keymaster.yml"

	// if you alresy use the context:
	simpleOidcAuth, err := authhandler.NewSimpleOIDCAuthFromConfig(&openidConfigFilename, nil)
	if err != nil {
		log.Println("Missing openidConfig file")
		panic(err)
	}
	authSource = simpleOidcAuth

	http.Handle(allgroupsPath, http.HandlerFunc(state.getallgroupsHandler))
	http.Handle(allusersPath, http.HandlerFunc(state.getallusersHandler))
	http.Handle(usergroupsPath, http.HandlerFunc(state.getgroupsofuserHandler))
	http.Handle(groupusersPath, http.HandlerFunc(state.getusersingroupHandler))

	http.Handle(creategroupWebPagePath, http.HandlerFunc(state.creategroupWebpageHandler))
	http.Handle(deletegroupWebPagePath, http.HandlerFunc(state.deletegroupWebpageHandler))
	http.Handle(creategroupPath, http.HandlerFunc(state.createGrouphandler))
	http.Handle(deletegroupPath, http.HandlerFunc(state.deleteGrouphandler))

	http.Handle(requestaccessPath, http.HandlerFunc(state.requestAccessHandler))
	http.Handle(indexPath, http.HandlerFunc(state.mygroupsHandler))
	http.Handle(authPath, simpleOidcAuth.Handler(http.HandlerFunc(state.mygroupsHandler)))
	http.Handle(allLDAPgroupsPath, http.HandlerFunc(state.allGroupsHandler))
	http.Handle(pendingactionsPath, http.HandlerFunc(state.pendingActions))
	http.Handle(pendingrequestsPath, http.HandlerFunc(state.pendingRequests))
	http.Handle(deleterequestsPath, http.HandlerFunc(state.deleteRequests))
	http.Handle(exitgroupPath, http.HandlerFunc(state.exitfromGroup))

	http.Handle(loginPath, simpleOidcAuth.Handler(http.HandlerFunc(state.loginHandler)))

	http.Handle(approverequestPath, http.HandlerFunc(state.approveHandler))
	http.Handle(rejectrequestPath, http.HandlerFunc(state.rejectHandler))

	http.Handle(addmembersPath, http.HandlerFunc(state.addmemberstoGroupWebpageHandler))
	http.Handle(addmembersbuttonPath, http.HandlerFunc(state.addmemberstoExistingGroup))

	http.Handle(deletemembersPath, http.HandlerFunc(state.deletemembersfromGroupWebpageHandler))
	http.Handle(deletemembersbuttonPath, http.HandlerFunc(state.deletemembersfromExistingGroup))

	http.Handle(createServiceAccWebPagePath, http.HandlerFunc(state.createserviceAccountPageHandler))
	http.Handle(createServiceAccountPath, http.HandlerFunc(state.createServiceAccounthandler))

	http.Handle(groupinfoPath, http.HandlerFunc(state.groupInfoWebpage))

	fs := http.FileServer(http.Dir(templatesdirectoryPath))
	http.Handle(cssPath, fs)
	http.Handle(imagesPath, fs)
	http.Handle(jsPath, fs)

	log.Fatal(http.ListenAndServeTLS(state.Config.Base.HttpAddress, state.Config.Base.TLSCertFilename, state.Config.Base.TLSKeyFilename, nil))
}
