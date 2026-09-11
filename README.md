# miniolt ONU Monitor

這是一個在 ZTE G7615V2 FTTR OLT 上執行的 ONU 監看工具，用來讀取連線中的 ONU 透過 OMCI 回報的硬體、軟體版本與其他識別資訊。

這個專案適合用來確認中華電信 ONU 在被更新後實際回報的版本，協助判斷自備 ONU 無法使用的原因。程式只擷取並解析 OLT 上的 `mini-olt` 封包，不會主動更新、刷寫或修改 ONU。

## 注意事項

- 建議先將裝置與正式網路隔離，並且不要把 Telnet 或 `8181` 連接埠暴露到 WAN。
- 頁面可能顯示 LOID 等敏感資訊，請不要將截圖或 API 輸出公開。
- 本工具的資料存在記憶體中，重啟後會清空；欄位是否有值取決於 ONU 是否送出對應的 OMCI 回應。

## 功能與限制

- 直接使用 Linux `AF_PACKET` raw socket 監聽 `mini-olt` 介面。
- 監聽 EtherType `0x0701`，解析 OMCI Get/MIB upload 回應。
- Web UI：`http://192.168.1.1:8181/`
- 清除目前記憶體資料：對 `/api/clear` 發送 `POST`。
- 目前 OLT IP 與介面名稱是編譯時寫死的：`192.168.1.1:8181` 與 `mini-olt`。若裝置使用不同 IP 或介面名稱，需先修改 `miniolt_onu_monitor.go` 後重新編譯。

## 需要的裝置與工具

- ZTE G7615V2 FTTR OLT。
- SC-SC 單模光纖線。
- 要觀察的 ONU。
- 一台可連到 G7615V2 LAN 的電腦。
- 電腦上的 Go、Python 3，以及 Telnet 用戶端。
- [Septrum101/zteOnu](https://github.com/Septrum101/zteOnu)，用來開啟 G7615V2 的 factory/Telnet mode。

## 完整操作流程

### 1. 準備並重設 G7615V2

1. 準備中國電信 FTTR 用的 ZTE G7615V2 與 SC-SC 光纖線。
2. 按下 G7615V2 的 `RESET` 鍵，等待重設及開機完成。
3. 將待觀察的 ONU 以 SC-SC 光纖線接到 G7615V2，並用電腦透過 LAN 連線至 OLT。
4. 確認電腦可以連到 `192.168.1.1`。後續指令中的 `<PC_IP>` 必須是 OLT 可以連回的電腦 IP。

### 2. 使用 zteOnu 開啟 root Telnet

`zteOnu` 會透過 G7615V2 的 `webFac` 流程開啟暫時的 factory Telnet，並驗證實際的 Telnet 登入；使用 `--telnet` 時，會在驗證成功後直接重新啟動 `telnetd`，不需要整台裝置重開機。

在電腦上取得 `zteOnu`，請選擇對應的作業系統下載：

[Release](https://github.com/Septrum101/zteOnu/releases)


確認電腦的網路介面可以通往 `192.168.1.1` 後執行：

```bash
./zteonu -i 192.168.1.1 --telnet
```

如果要讓裝置透過重開機套用永久 Telnet，也可以使用：

```bash
./zteonu -i 192.168.1.1 --telnet-restart
```

目前 `zteOnu` README 使用的 factory mode 預設值為：

- HTTP：`192.168.1.1:8080`
- factory mode 帳號：`telecomadmin`
- factory mode 密碼：`nE7jA%5m`
- Telnet：連接埠 `23`
- root Telnet 帳號：`root`
- root Telnet 密碼：`Zte521`

工具通常會自動處理 factory mode 帳號與密碼，不需要手動呼叫 webFac。成功後可測試 root Telnet：

```bash
telnet 192.168.1.1
```

登入時輸入：

```text
帳號：root
密碼：Zte521
```

看到 `$` 提示符表示 root Telnet 已可用。

### 3. 編譯 Linux ARM64 執行檔

本專案目前是單一 Go 原始碼檔案，建議使用 `CGO_ENABLED=0` 產生不依賴系統 C library 的 ARM64 Linux 執行檔。

Linux 或 macOS：

```bash
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 \
  go build -trimpath -o miniolt-onu-monitor-arm64 miniolt_onu_monitor.go
```

Windows PowerShell：

```powershell
$env:GOOS = "linux"
$env:GOARCH = "arm64"
$env:CGO_ENABLED = "0"
go build -trimpath -o miniolt-onu-monitor-arm64 .\miniolt_onu_monitor.go
```

在 Linux/macOS 上可用 `file` 確認編譯結果：

```bash
file miniolt-onu-monitor-arm64
```

結果應該包含 `ARM aarch64`、`ARM64` 或相近字樣。Go 也可以讀取執行檔內的 build information：

```bash
go version -m miniolt-onu-monitor-arm64
```

應確認至少包含：

```text
CGO_ENABLED=0
GOARCH=arm64
GOOS=linux
```

#### GitHub Release 方式

`miniolt-onu-monitor-arm64` 是編譯結果，請不要將它 commit 到原始碼儲存庫。請將下列內容 push 到儲存庫：

- `README.md`
- `miniolt_onu_monitor.go`
- `.gitignore`

再在 GitHub 建立 Release，將檔名完全相同的 `miniolt-onu-monitor-arm64` 上傳為 Release 附件。部署前先從該 Release 下載執行檔到電腦，再用下一節的 Python HTTP 伺服器傳入 G7615V2。

### 4. 進入 framework container 並傳送執行檔

先從電腦 Telnet 進入 G7615V2：

```bash
telnet 192.168.1.1
```

輸入 root 帳號與密碼後，在 root shell 執行：

```bash
saf console
```

提示輸入密碼時輸入 `upt`。輸入時可能不會回顯文字，直接輸入後按 Enter；成功時通常會看到：

```text
Connected to tty 0, timeout 60s
```

這代表已進入 framework container。請保留這個 Telnet 視窗，再於電腦另開一個終端機，切換到存放 Release 執行檔的資料夾並啟動 HTTP 伺服器：

```bash
python -m http.server 8000
```

如果電腦有多張網路卡，請確認伺服器使用的是 G7615V2 可連線的網路卡；Windows 防火牆也必須允許 TCP `8000`。接著回到 framework container，將 `<PC_IP>` 替換成電腦 IP：

```bash
cd /opt/apps
wget -O /opt/apps/miniolt-onu-monitor-arm64 \
  http://<PC_IP>:8000/miniolt-onu-monitor-arm64
chmod 755 /opt/apps/miniolt-onu-monitor-arm64
ls -l /opt/apps/miniolt-onu-monitor-arm64
```

### 5. 修改 appmgr，讓 monitor 開機自動執行

以下操作必須在 `saf console` 進入的 framework container 內完成。先備份原始腳本，再開啟 `appmgr`：

```bash
cd /etc/init.d
ls
vi appmgr
```

進入 `vi` 後：

1. 按 `i` 進入編輯模式。
2. 找到 `start_service()`，將原本的 4 行 `procd` 指令註解掉。
3. 在函式內加入 `/opt/apps/miniolt-onu-monitor-arm64 &`，**行尾的 `&` 絕對不能省略**。
4. 按 `Esc`，輸入 `:wq`，再按 Enter 儲存離開。

修改後的內容應類似：

```sh
start_service(){
#       procd_open_instance
#       procd_set_param command "$PROG"
#       procd_set_param respawn 3600 5 0
#       procd_close_instance
        /opt/apps/miniolt-onu-monitor-arm64 &
}
```

> **重要：請務必保留指令最後的 `&`。** `&` 會讓 monitor 在背景執行；如果省略，程式會以前景執行並卡住 `appmgr` 的啟動流程，G7615V2 可能會在開機時卡住，甚至無法正常開機。

只修改 `start_service()` 內的內容，其他函式與腳本內容請保留。用以下指令確認修改結果：

```bash
cat /etc/init.d/appmgr | grep start_service -A 5
```

輸出應看得到 4 行 `procd` 都已加上 `#`，並且包含 monitor 的完整路徑與行尾的 `&`。

### 6. 離開 saf console 並重開機

在 `saf console` 視窗按下列按鍵離開 console：

```text
Ctrl + a
```

放開按鍵後再按：

```text
q
```

回到 root shell 後重開 G7615V2：

```bash
reboot
```

等待 OLT 完成開機，Telnet 連線中斷是正常現象。

### 7. 讀取 ONU 版本

用電腦瀏覽器開啟：

```text
http://192.168.1.1:8181/
```

網頁會顯示目前收到的 ONU 資訊，包括：

- ONU ID
- Vendor、Serial、MAC
- Equipment / Model、Hardware
- Software Image 1、Software Image 2
- Product Class
- LOID 與 LOID Password（若封包中有觀察到）
- Last Seen

也可以直接查 JSON API：

```bash
curl http://192.168.1.1:8181/api/onus
```

如果剛開機時沒有資料，重新插拔 ONU，再等待新的 OMCI 回應。

## 疑難排解

### `exec format error`

代表執行檔不是 Linux ARM64。重新編譯時確認 `GOOS=linux`、`GOARCH=arm64`、`CGO_ENABLED=0`，並用 `file` 或 `go version -m` 檢查。

### `8181` 無法連線

在 framework container 內確認檔案存在且可執行：

```bash
ls -l /opt/apps/miniolt-onu-monitor-arm64
ps | grep '[m]iniolt-onu-monitor-arm64'
```

若程序沒有執行，檢查 `appmgr` 是否改在 framework container 內、路徑是否完全一致、指令行尾是否保留 `&`，以及是否真的完成重開機。若 OLT 管理 IP 不是 `192.168.1.1`，需修改原始碼中的 `listenAddr` 後重新編譯。

### Python HTTP 伺服器或 `wget` 失敗

- `<PC_IP>` 必須是 G7615V2 可達的電腦 IP，不是 `127.0.0.1`。
- 確認電腦的 TCP `8000` 沒被防火牆阻擋。
- 確認 Python 伺服器的目前資料夾內確實有 `miniolt-onu-monitor-arm64`。
- 在電腦瀏覽器先測試 `http://<PC_IP>:8000/` 是否能看到檔案列表。

### Web UI 沒有 ONU 或版本欄位是空白

- 確認 ONU 已連接到 G7615V2 且有新的 OMCI 流量。
- 確認介面名稱仍是 `mini-olt`；目前程式沒有提供 runtime 參數。
- 空白欄位表示目前尚未觀察到對應資料，不代表一定沒有該欄位。
- 重啟 monitor 會清除記憶體中的資料，需等待下一輪 OMCI 回應。

### zteOnu 無法驗證 Telnet

確認電腦是直接或透過正確 VLAN/路由連到 `192.168.1.1`。若裝置不接受用戶端 MAC，依 `zteOnu` 的流程改用 `--iface` 或 `--mac`；HTTP factory 流程成功不代表 Telnet 登入一定成功，必須以實際 `root`/`Zte521` 登入驗證。

## 儲存庫與 Release 內容

原始碼儲存庫不應包含 ARM64 執行檔：

```text
.
├── .gitignore
├── README.md
└── miniolt_onu_monitor.go
```

`miniolt-onu-monitor-arm64` 請只放在 GitHub Release 的附件中，部署時下載 Release 附件後再傳入 `/opt/apps/miniolt-onu-monitor-arm64`。修改 `appmgr` 時，請務必保留執行檔指令結尾的 `&`，否則裝置可能無法完成開機。
