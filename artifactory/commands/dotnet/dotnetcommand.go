package dotnet

import (
	"fmt"
	"github.com/jfrog/gofrog/io"
	"github.com/jfrog/jfrog-cli/artifactory/utils"
	dotnet "github.com/jfrog/jfrog-cli/artifactory/utils/dotnet"
	"github.com/jfrog/jfrog-cli/artifactory/utils/dotnet/solution"
	"github.com/jfrog/jfrog-cli/utils/config"
	"github.com/jfrog/jfrog-client-go/auth"
	"github.com/jfrog/jfrog-client-go/utils/errorutils"
	"github.com/jfrog/jfrog-client-go/utils/io/fileutils"
	"github.com/jfrog/jfrog-client-go/utils/log"
	"io/ioutil"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
)

const SourceName = "JFrogCli"

type DotnetCommand struct {
	toolchainType      dotnet.ToolchainType
	subCommand         string
	argAndFlags        string
	repoName           string
	solutionPath       string
	buildConfiguration *utils.BuildConfiguration
	rtDetails          *config.ArtifactoryDetails
}

func (dc *DotnetCommand) SetRtDetails(rtDetails *config.ArtifactoryDetails) *DotnetCommand {
	dc.rtDetails = rtDetails
	return dc
}

func (dc *DotnetCommand) SetBuildConfiguration(buildConfiguration *utils.BuildConfiguration) *DotnetCommand {
	dc.buildConfiguration = buildConfiguration
	return dc
}

func (dc *DotnetCommand) SetToolchainType(toolchainType dotnet.ToolchainType) *DotnetCommand {
	dc.toolchainType = toolchainType
	return dc
}

func (dc *DotnetCommand) SetSolutionPath(solutionPath string) *DotnetCommand {
	dc.solutionPath = solutionPath
	return dc
}

func (dc *DotnetCommand) SetRepoName(repoName string) *DotnetCommand {
	dc.repoName = repoName
	return dc
}

func (dc *DotnetCommand) SetArgAndFlags(argAndFlags string) *DotnetCommand {
	dc.argAndFlags = argAndFlags
	return dc
}

func (dc *DotnetCommand) SetBasicCommand(subCommand string) *DotnetCommand {
	dc.subCommand = subCommand
	return dc
}

func (dc *DotnetCommand) RtDetails() (*config.ArtifactoryDetails, error) {
	return dc.rtDetails, nil
}

func (dc *DotnetCommand) CommandName() string {
	return "rt_" + dc.toolchainType.String()
}

// Exec all consume type nuget commands, install, update, add, restore.
func (dc *DotnetCommand) Exec() error {
	log.Info("Running " + dc.toolchainType.String() + "...")
	// Use temp dir to save config file, so that config will be removed at the end.
	tempDirPath, err := fileutils.CreateTempDir()
	if err != nil {
		return err
	}
	defer fileutils.RemoveTempDir(tempDirPath)

	dc.solutionPath, err = changeWorkingDir(dc.solutionPath)
	if err != nil {
		return err
	}

	err = dc.prepareAndRunCmd(tempDirPath)
	if err != nil {
		return err
	}

	isCollectBuildInfo := len(dc.buildConfiguration.BuildName) > 0 && len(dc.buildConfiguration.BuildNumber) > 0
	if !isCollectBuildInfo {
		return nil
	}

	slnFile, err := dc.updateSolutionPathAndGetFileName()
	if err != nil {
		return err
	}
	sol, err := solution.Load(dc.solutionPath, slnFile)
	if err != nil {
		return err
	}

	if err = utils.SaveBuildGeneralDetails(dc.buildConfiguration.BuildName, dc.buildConfiguration.BuildNumber); err != nil {
		return err
	}
	buildInfo, err := sol.BuildInfo(dc.buildConfiguration.Module)
	if err != nil {
		return err
	}
	return utils.SaveBuildInfo(dc.buildConfiguration.BuildName, dc.buildConfiguration.BuildNumber, buildInfo)
}

func (dc *DotnetCommand) updateSolutionPathAndGetFileName() (string, error) {
	argsAndFlags := strings.Split(dc.argAndFlags, " ")
	cmdFirstArg := argsAndFlags[0]
	// The path argument wasn't provided, sln file will be searched under working directory.
	if len(cmdFirstArg) == 0 || strings.HasPrefix(cmdFirstArg, "-") {
		return "", nil
	}
	exist, err := fileutils.IsDirExists(cmdFirstArg, true)
	if err != nil {
		return "", err
	}
	// The path argument is a directory. sln/csproj file will be searched under this directory.
	if exist {
		dc.updateSolutionPath(cmdFirstArg)
		return "", err
	}
	exist, err = fileutils.IsFileExists(cmdFirstArg, true)
	if err != nil {
		return "", err
	}
	if exist {
		// The path argument is a .sln file.
		if strings.HasSuffix(cmdFirstArg, ".sln") {
			dc.updateSolutionPath(filepath.Dir(cmdFirstArg))
			return filepath.Base(cmdFirstArg), nil
		}
		// The path argument is a .csproj/packages.config file.
		if strings.HasSuffix(cmdFirstArg, ".csproj") || strings.HasSuffix(cmdFirstArg, "packages.config") {
			dc.updateSolutionPath(filepath.Dir(cmdFirstArg))
		}
	}
	return "", nil
}

func (dc *DotnetCommand) updateSolutionPath(slnRootPath string) {
	if filepath.IsAbs(slnRootPath) {
		dc.solutionPath = slnRootPath
	} else {
		dc.solutionPath = filepath.Join(dc.solutionPath, slnRootPath)
	}
}

// Changes the working directory if provided.
// Returns the path to the solution
func changeWorkingDir(newWorkingDir string) (string, error) {
	var err error
	if newWorkingDir != "" {
		err = os.Chdir(newWorkingDir)
	} else {
		newWorkingDir, err = os.Getwd()
	}

	return newWorkingDir, errorutils.CheckError(err)
}

// Prepares the nuget configuration file within the temp directory
// Runs NuGet itself with the arguments and flags provided.
func (dc *DotnetCommand) prepareAndRunCmd(configDirPath string) error {
	cmd, err := dc.createCmd()
	if err != nil {
		return err
	}
	// To prevent NuGet prompting for credentials
	err = os.Setenv("NUGET_EXE_NO_PROMPT", "true")
	if err != nil {
		return errorutils.CheckError(err)
	}

	err = dc.prepareConfigFile(cmd, configDirPath)
	if err != nil {
		return err
	}
	err = io.RunCmd(cmd)
	if err != nil {
		return err
	}

	return nil
}

// Checks if the user provided input such as -configfile flag or -Source flag.
// If those flags were provided, NuGet will use the provided configs (default config file or the one with -configfile)
// If neither provided, we are initializing our own config.
func (dc *DotnetCommand) prepareConfigFile(cmd *dotnet.Cmd, configDirPath string) error {
	cmdFlag := cmd.GetToolchain().GetTypeFlagPrefix() + "configfile"
	currentConfigPath, err := getFlagValueIfExists(cmdFlag, cmd)
	if err != nil {
		return err
	}
	if currentConfigPath != "" {
		return nil
	}

	cmdFlag = cmd.GetToolchain().GetTypeFlagPrefix() + "source"
	sourceCommandValue, err := getFlagValueIfExists(cmdFlag, cmd)
	if err != nil {
		return err
	}
	if sourceCommandValue != "" {
		return nil
	}

	err = dc.initNewConfig(cmd, configDirPath)
	return err
}

// Returns the value of the flag if exists
func getFlagValueIfExists(cmdFlag string, cmd *dotnet.Cmd) (string, error) {
	for i := 0; i < len(cmd.CommandFlags); i++ {
		if !strings.EqualFold(cmd.CommandFlags[i], cmdFlag) {
			continue
		}
		if i+1 == len(cmd.CommandFlags) {
			return "", errorutils.CheckError(errorutils.CheckError(fmt.Errorf(cmdFlag, " flag was provided without value")))
		}
		return cmd.CommandFlags[i+1], nil
	}

	return "", nil
}

// Initializing a new NuGet config file that NuGet will use into a temp file
func (dc *DotnetCommand) initNewConfig(cmd *dotnet.Cmd, configDirPath string) error {
	// Got to here, means that neither of the flags provided and we need to init our own config.
	configFile, err := WriteToTempConfigFile(cmd, configDirPath)
	if err != nil {
		return err
	}

	return dc.AddNugetAuthToConfig(cmd.GetToolchain(), configFile)
}

// Runs nuget add sources command to authenticate with Artifactory.
func (dc *DotnetCommand) AddNugetAuthToConfig(cmdType dotnet.ToolchainType, configFile *os.File) error {
	sourceUrl, user, password, err := dc.getSourceDetails()
	if err != nil {
		return err
	}

	return addSourceToNugetConfig(cmdType, configFile.Name(), sourceUrl, user, password)
}

// Creates the temp file and writes the config template into the file for NuGet can use it.
func WriteToTempConfigFile(cmd *dotnet.Cmd, tempDirPath string) (*os.File, error) {
	configFile, err := ioutil.TempFile(tempDirPath, "jfrog.cli.nuget.")
	if err != nil {
		return nil, errorutils.CheckError(err)
	}
	log.Debug("Nuget config file created at:", configFile.Name())

	defer configFile.Close()

	cmd.CommandFlags = append(cmd.CommandFlags, cmd.GetToolchain().GetTypeFlagPrefix()+"configfile", configFile.Name())

	// Set Artifactory repo as source
	content := dotnet.ConfigFileTemplate
	_, err = configFile.WriteString(content)
	if err != nil {
		return nil, errorutils.CheckError(err)
	}
	return configFile, nil
}

// Runs nuget sources add command
func addSourceToNugetConfig(cmdType dotnet.ToolchainType, configFileName, sourceUrl, user, password string) error {
	cmd, err := dotnet.CreateDotnetAddSourceCmd(cmdType, sourceUrl)
	if err != nil {
		return err
	}

	flagPrefix := cmdType.GetTypeFlagPrefix()
	cmd.CommandFlags = append(cmd.CommandFlags, flagPrefix+"configfile", configFileName)
	cmd.CommandFlags = append(cmd.CommandFlags, flagPrefix+"name", SourceName)
	cmd.CommandFlags = append(cmd.CommandFlags, flagPrefix+"username", user)
	cmd.CommandFlags = append(cmd.CommandFlags, flagPrefix+"password", password)
	output, err := io.RunCmdOutput(cmd)
	log.Debug("Running command: Add sources. Output:", output)
	return err
}

func (dc *DotnetCommand) getSourceDetails() (sourceURL, user, password string, err error) {
	var u *url.URL
	u, err = url.Parse(dc.rtDetails.Url)
	if errorutils.CheckError(err) != nil {
		return
	}
	u.Path = path.Join(u.Path, "api/nuget", dc.repoName)
	sourceURL = u.String()

	user = dc.rtDetails.User
	password = dc.rtDetails.Password
	// If access-token is defined, extract user from it.
	rtDetails, err := dc.RtDetails()
	if errorutils.CheckError(err) != nil {
		return
	}
	if rtDetails.AccessToken != "" {
		log.Debug("Using access-token details for nuget authentication.")
		user, err = auth.ExtractUsernameFromAccessToken(rtDetails.AccessToken)
		if err != nil {
			return
		}
		password = rtDetails.AccessToken
	}
	return
}

func (dc *DotnetCommand) createCmd() (*dotnet.Cmd, error) {
	c, err := dotnet.NewToolchainCmd(dc.toolchainType)
	if err != nil {
		return nil, err
	}
	if dc.subCommand != "" {
		c.Command, err = utils.ParseArgs(strings.Split(dc.subCommand, " "))
		if err != nil {
			return nil, errorutils.CheckError(err)
		}
	}

	if dc.argAndFlags != "" {
		c.CommandFlags, err = utils.ParseArgs(strings.Split(dc.argAndFlags, " "))
	}

	return c, errorutils.CheckError(err)
}
