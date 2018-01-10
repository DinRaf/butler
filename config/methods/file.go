package methods

import (
	"bytes"
	"errors"
	"fmt"
	"io/ioutil"

	"git.corp.adobe.com/TechOps-IAO/butler/environment"

	//log "github.com/sirupsen/logrus"
	"github.com/spf13/viper"
)

func NewFileMethod(manager *string, entry *string) (Method, error) {
	var (
		err    error
		result FileMethod
	)

	if (manager != nil) && (entry != nil) {
		err = viper.UnmarshalKey(*entry, &result)
		if err != nil {
			return result, err
		}
	}

	if environment.GetVar(result.Path) != "" {
		result.Path = environment.GetVar(result.Path)
	}
	return result, err
}

func NewFileMethodWithPath(file string) (Method, error) {
	var (
		err    error
		result FileMethod
	)

	result.Path = file
	return result, err
}

type FileMethod struct {
	Path string `mapstructure:"path" json:"path"`
}

func (f FileMethod) Get(file string) (*Response, error) {
	var (
		err      error
		fileData []byte
		response Response
	)

	fileData, err = ioutil.ReadFile(fmt.Sprintf("%s/%s", f.Path, file))

	if err != nil {
		// 504 is hokey, but we need some bogus code.
		return &Response{statusCode: 504}, errors.New(fmt.Sprintf("FileMethod.Get(): caught error read file err=%v", err.Error()))
	}

	response.statusCode = 200
	response.body = ioutil.NopCloser(bytes.NewReader(fileData))
	return &response, nil
}
