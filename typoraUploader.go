package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/h2non/filetype"
	"github.com/urfave/cli/v2"
	"gopkg.in/ini.v1"
)

var (
	noaHandlerAddr, NoaHandlerToken, Bucket, Workflow, StorageType string
)

func typoraUploaderCLI() {
	typoraUploader := &cli.App{
		Name: "typoraUploader",
		Usage: "typora 图片上传插件，上传图片到 NoaHandler 平台" +
			"\nPowered By Luckykeeper <luckykeeper@luckykeeper.site | https://luckykeeper.site>" +
			"\n————————————————————————————————————————" +
			"\n注意：使用前需要先填写同目录下 config.ini !",
		Version: "1.0.0_build20230606",
		Commands: []*cli.Command{
			{
				Name:    "upload",
				Aliases: []string{"u"},
				Usage:   "上传文件到指定路径",
				Action: func(cCtx *cli.Context) error {

					exeFilePath, _ := os.Executable()
					workDir := exeFilePath[:strings.LastIndex(exeFilePath, "\\")]
					workDir = strings.ReplaceAll(workDir, "\\", "/")

					readConfig(workDir)
					// 判断 webp 依赖库
					if bool, _ := pathExists(workDir + "/libwebp/bin/cwebp.exe"); !bool {
						log.Fatalln("未找到\"" + workDir + "/libwebp/bin/cwebp.exe\"\n请先下载 webp 依赖库，并放置在指定位置!")
					}
					// 转换上传任务流
					uploadPathList := cCtx.Args().Slice()
					filePathList, deleteList := picWebpWorkflow(uploadPathList, workDir)
					fileUrls := uploadToNoaHandler(filePathList)
					// 删除缓存文件
					for _, file := range deleteList {
						os.Remove(file)
					}
					// 输出结果
					for _, file := range fileUrls {
						fmt.Println(file)
					}
					return nil
				},
			},
		},
	}

	if err := typoraUploader.Run(os.Args); err != nil {
		log.Fatal(err)
	}
}

// read config.ini
func readConfig(workDir string) {
	configFile, err := ini.Load(workDir + "/config.ini")
	if err != nil {
		log.Panicln("读取配置文件 config.ini 失败，失败原因为: ", err)
		os.Exit(1)
	}
	noaHandlerAddr = configFile.Section("typoraUploader").Key("noaHandlerAddr").String()
	NoaHandlerToken = configFile.Section("typoraUploader").Key("token").String()
	Bucket = configFile.Section("typoraUploader").Key("bucket").String()
	Workflow = configFile.Section("typoraUploader").Key("workflow").String()
	StorageType = configFile.Section("typoraUploader").Key("storageType").String()
}

func main() {
	typoraUploaderCLI()
}

// 图片判断与转换任务流，返回图片路径及要删除的文件路径，若转换下一环节上传完应当删除转换过的文件
func picWebpWorkflow(uploadPathList []string, workDir string) (filePathList, deleteList []string) {
	for i, filePath := range uploadPathList {
		// 判断文件类型，若为 webp 可直接上传
		needConvert := checkFileHeader(filePath)
		if needConvert {
			// 对图像进行 webp 转换
			// .\libwebp\bin\cwebp.exe -q 75 -m 6 -mt .\E2E912F7717BF4FD0BBFF615207F6CFC.jpg -o .\E2E912F7717BF4FD0BBFF615207F6CFC.webp
			newFileName := workDir + "/" + strconv.Itoa(i+1) + ".webp"
			// 注意参数不能用空格分开，如：-q 75 是错误的
			command := exec.Command(workDir+"/libwebp/bin/cwebp.exe", "-q", "75", "-m", "6", "-mt", filePath, "-o", newFileName)
			command.Run()

			deleteList = append(deleteList, newFileName)
			filePathList = append(filePathList, newFileName)
		} else {
			// 直接上传，且不进入删除列表
			filePathList = append(filePathList, filePath)
		}
	}
	return
}

// 上传任务流
func uploadToNoaHandler(uploadList []string) (picUrls []string) {
	for _, file := range uploadList {
		for {
			serverReturn := uploadFileToUshioNoa(file)
			if serverReturn.StatusCode != 200 {
				continue
			} else {
				picUrls = append(picUrls, serverReturn.FileUrl)
				break
			}
		}
	}
	return
}

// 检查文件类型
func checkFileHeader(filePath string) (needConvert bool) {
	// buf, _ := ioutil.ReadFile(filePath)
	buf, _ := os.ReadFile(filePath)
	kind, _ := filetype.Match(buf)
	fileType := kind.Extension

	// 白名单
	// 图片：jpg png webp
	if fileType == "jpg" || fileType == "png" {
		needConvert = true
		return
	} else if fileType == "webp" {
		needConvert = false
		return
	} else {
		log.Fatalln("UnSupported FlieType! Or File No Exists!")
		return
	}
}

// 判断文件是否存在
func pathExists(path string) (bool, error) {
	_, err := os.Stat(path)
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, err
}

// post 方式上传文件（单文件）
// 根据现成的封装再封一层
func uploadFileToUshioNoa(filePath string) (serverReturn UshioNoaUploadFileResult) {

	file := []UploadFile{
		{Name: "file", Filepath: filePath},
	}

	gatewayRequestUrl := noaHandlerAddr + "uploadFile"

	reqParams := map[string]string{"token": NoaHandlerToken,
		"storageType": "s3",
		"bucket":      Bucket,
		"workFlow":    Workflow}
	response := PostFile(gatewayRequestUrl, reqParams, file, map[string]string{"User-Agent": "typoraUploader"})
	json.Unmarshal(response, &serverReturn)
	return serverReturn
}

type UploadFile struct {
	// 表单名称
	Name string
	// 文件全路径
	Filepath string
}

// 请求客户端
var httpClient = &http.Client{}

func PostFile(reqUrl string, reqParams map[string]string, files []UploadFile, headers map[string]string) []byte {
	return post(reqUrl, reqParams, "multipart/form-data", files, headers)
}

func post(reqUrl string, reqParams map[string]string, contentType string, files []UploadFile, headers map[string]string) []byte {
	requestBody, realContentType := getReader(reqParams, contentType, files)
	httpRequest, _ := http.NewRequest("POST", reqUrl, requestBody)
	// 添加请求头
	httpRequest.Header.Add("Content-Type", realContentType)
	for k, v := range headers {
		httpRequest.Header.Add(k, v)
	}
	// 发送请求
	resp, err := httpClient.Do(httpRequest)
	if err != nil {
		panic(err)
	}
	defer resp.Body.Close()
	response, _ := io.ReadAll(resp.Body)
	return response
}

func getReader(reqParams map[string]string, contentType string, files []UploadFile) (io.Reader, string) {
	if strings.Contains(contentType, "json") {
		bytesData, _ := json.Marshal(reqParams)
		return bytes.NewReader(bytesData), contentType
	} else if files != nil {
		body := &bytes.Buffer{}
		// 文件写入 body
		writer := multipart.NewWriter(body)
		for _, uploadFile := range files {
			file, err := os.Open(uploadFile.Filepath)
			if err != nil {
				panic(err)
			}
			part, err := writer.CreateFormFile(uploadFile.Name, filepath.Base(uploadFile.Filepath))
			if err != nil {
				panic(err)
			}
			io.Copy(part, file)
			file.Close()
		}
		// 其他参数列表写入 body
		for k, v := range reqParams {
			if err := writer.WriteField(k, v); err != nil {
				panic(err)
			}
		}
		if err := writer.Close(); err != nil {
			panic(err)
		}
		// 上传文件需要自己专用的contentType
		return body, writer.FormDataContentType()
	} else {
		urlValues := url.Values{}
		for key, val := range reqParams {
			urlValues.Set(key, val)
		}
		reqBody := urlValues.Encode()
		return strings.NewReader(reqBody), contentType
	}
}

// 文件上传接口 - 返回
type UshioNoaUploadFileResult struct {
	StatusCode   int    `json:"statusCode"` // 结果码 （操作成功200，Token错误401）
	StatusString string `json:"StatusString"`
	FileUrl      string `json:"fileUrl"`
}
