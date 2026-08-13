const form = document.querySelector("#convert-form");
const setupGuide = document.querySelector("#convert-form .setup-guide");
const input = document.querySelector("#pack-input");
const apiKeyInput = document.querySelector("#api-key-input");
const userIDInput = document.querySelector("#user-id-input");
const rememberCredentials = document.querySelector("#remember-credentials");
const dropZone = document.querySelector("#drop-zone");
const selectedFile = document.querySelector("#selected-file");
const fileName = document.querySelector("#file-name");
const fileSize = document.querySelector("#file-size");
const removeFile = document.querySelector("#remove-file");
const convertButton = document.querySelector("#convert-button");
const progressPanel = document.querySelector("#progress-panel");
const progressTrack = document.querySelector("#progress-track");
const progressBar = document.querySelector("#progress-bar");
const statusLabel = document.querySelector("#status-label");
const statusDetail = document.querySelector("#status-detail");
const statusPercent = document.querySelector("#status-percent");
const statusError = document.querySelector("#status-error");
const activityLog = document.querySelector("#activity-log");
const resultPanel = document.querySelector("#result-panel");
const jsonOutput = document.querySelector("#json-output");
const copyButton = document.querySelector("#copy-button");

let currentFile = null;
let resultJSON = "";

const storageKeys = {
  remember: "cone.rememberCredentials",
  apiKey: "cone.robloxApiKey",
  userID: "cone.robloxUserId",
};

function readSavedCredentials() {
  try {
    if (localStorage.getItem(storageKeys.remember) === "false") {
      rememberCredentials.checked = false;
      return;
    }
    rememberCredentials.checked = true;
    apiKeyInput.value = localStorage.getItem(storageKeys.apiKey) || "";
    userIDInput.value = localStorage.getItem(storageKeys.userID) || "";
    if (apiKeyInput.value && userIDInput.value) setupGuide.open = false;
  } catch {
    rememberCredentials.checked = false;
  }
}

function saveCredentials() {
  try {
    if (!rememberCredentials.checked) {
      localStorage.setItem(storageKeys.remember, "false");
      localStorage.removeItem(storageKeys.apiKey);
      localStorage.removeItem(storageKeys.userID);
      return;
    }
    localStorage.setItem(storageKeys.remember, "true");
    localStorage.setItem(storageKeys.apiKey, apiKeyInput.value);
    localStorage.setItem(storageKeys.userID, userIDInput.value);
  } catch {
    rememberCredentials.checked = false;
  }
}

function formatBytes(bytes) {
  if (bytes < 1024) return `${bytes} B`;
  const units = ["KB", "MB", "GB"];
  let value = bytes / 1024;
  let unit = units[0];
  for (let index = 1; value >= 1024 && index < units.length; index += 1) {
    value /= 1024;
    unit = units[index];
  }
  return `${value.toFixed(value >= 10 ? 1 : 2)} ${unit}`;
}

function setFile(file) {
  if (!file) {
    currentFile = null;
    input.value = "";
    selectedFile.hidden = true;
    updateConvertState();
    return;
  }
  if (!file.name.toLowerCase().endsWith(".zip")) {
    showError("Choose a Minecraft texture-pack ZIP file.");
    return;
  }
  currentFile = file;
  fileName.textContent = file.name;
  fileSize.textContent = formatBytes(file.size);
  selectedFile.hidden = false;
  updateConvertState();
  resetOutput();
}

function updateConvertState() {
  convertButton.disabled = !currentFile || !apiKeyInput.value.trim() || !userIDInput.value.trim();
}

function resetOutput() {
  progressPanel.hidden = true;
  resultPanel.hidden = true;
  statusError.hidden = true;
  activityLog.replaceChildren();
  resultJSON = "";
  jsonOutput.textContent = "";
  copyButton.textContent = "Copy JSON";
}

function appendLog(message, isError = false) {
  if (!message) return;
  const line = document.createElement("p");
  line.textContent = message;
  line.classList.toggle("is-error", isError);
  activityLog.append(line);
  activityLog.scrollTop = activityLog.scrollHeight;
}

function updateProgress(percent, label, detail) {
  const safePercent = Math.max(0, Math.min(100, Math.round(percent)));
  progressPanel.hidden = false;
  progressBar.style.width = `${safePercent}%`;
  progressTrack.setAttribute("aria-valuenow", String(safePercent));
  statusPercent.textContent = `${safePercent}%`;
  statusLabel.textContent = label;
  statusDetail.textContent = detail;
}

function showError(message) {
  progressPanel.hidden = false;
  resultPanel.hidden = true;
  statusLabel.textContent = "Couldn’t convert pack";
  statusDetail.textContent = "Check the file and try again.";
  statusError.textContent = message;
  statusError.hidden = true;
  appendLog(message, true);
}

function handleProgress(progress) {
  statusError.hidden = true;
  const message = progress.message || progress.name || "Working…";
  if (progress.stage === "receiving") {
    updateProgress(4, "Porting...", message);
    appendLog(message);
    return;
  }
  if (progress.stage === "preparing") {
    updateProgress(12, "Porting...", message);
    appendLog(message);
    return;
  }
  if (progress.stage === "uploading") {
    const ratio = progress.total ? progress.completed / progress.total : 0;
    updateProgress(12 + ratio * 83, "Porting...", message);
    appendLog(message, Boolean(progress.error));
    return;
  }
  if (progress.stage === "complete") {
    updateProgress(100, "Ported", message);
    appendLog(message);
    return;
  }
  if (progress.stage === "notifying") {
    updateProgress(100, "Ported", message);
    appendLog(progress.error ? `${message}: ${progress.error}` : message, Boolean(progress.error));
  }
}

function handleResult(event) {
  resultJSON = JSON.stringify(event.result);
  jsonOutput.textContent = resultJSON;
  resultPanel.hidden = false;
}

async function copyJSON() {
  if (!resultJSON) return;
  try {
    await navigator.clipboard.writeText(resultJSON);
  } catch {
    const selection = window.getSelection();
    const range = document.createRange();
    range.selectNodeContents(jsonOutput);
    selection.removeAllRanges();
    selection.addRange(range);
    document.execCommand("copy");
    selection.removeAllRanges();
  }
  copyButton.textContent = "Copied";
  window.setTimeout(() => {
    copyButton.textContent = "Copy JSON";
  }, 1600);
}

async function consumeEvents(response) {
  if (!response.ok) {
    const responseText = (await response.text()).trim();
    const contentType = response.headers.get("content-type") || "";
    const isHTML = contentType.includes("text/html") || /(?:<!?doctype\s+html|<html)/i.test(responseText);
    if (isHTML) {
      throw new Error(`Cone server is temporarily unavailable (${response.status}). Try again shortly.`);
    }
    throw new Error(responseText || `Server returned ${response.status}`);
  }
  if (!response.body) {
    throw new Error("This browser cannot read conversion progress.");
  }
  const reader = response.body.getReader();
  const decoder = new TextDecoder();
  let buffered = "";
  while (true) {
    const { value, done } = await reader.read();
    buffered += decoder.decode(value || new Uint8Array(), { stream: !done });
    const lines = buffered.split("\n");
    buffered = lines.pop() || "";
    for (const line of lines) {
      if (!line.trim()) continue;
      const event = JSON.parse(line);
      if (event.type === "progress") handleProgress(event.progress);
      if (event.type === "result") handleResult(event);
      if (event.type === "error") throw new Error(event.message || "Conversion failed.");
    }
    if (done) break;
  }
}

input.addEventListener("change", () => setFile(input.files[0]));
removeFile.addEventListener("click", () => setFile(null));
copyButton.addEventListener("click", copyJSON);
apiKeyInput.addEventListener("input", () => {
  saveCredentials();
  updateConvertState();
});
userIDInput.addEventListener("input", () => {
  saveCredentials();
  updateConvertState();
});
rememberCredentials.addEventListener("change", saveCredentials);

readSavedCredentials();
updateConvertState();

for (const eventName of ["dragenter", "dragover"]) {
  dropZone.addEventListener(eventName, (event) => {
    event.preventDefault();
    dropZone.classList.add("is-dragging");
  });
}
for (const eventName of ["dragleave", "drop"]) {
  dropZone.addEventListener(eventName, (event) => {
    event.preventDefault();
    dropZone.classList.remove("is-dragging");
  });
}
dropZone.addEventListener("drop", (event) => setFile(event.dataTransfer.files[0]));

form.addEventListener("submit", async (event) => {
  event.preventDefault();
  if (!currentFile) return;
  setupGuide.open = false;
  resetOutput();
  convertButton.disabled = true;
  removeFile.disabled = true;
  apiKeyInput.disabled = true;
  userIDInput.disabled = true;
  updateProgress(1, "Porting...", `Sending ${currentFile.name}`);
  appendLog(`Sending ${currentFile.name}`);
  const body = new FormData();
  body.append("pack", currentFile, currentFile.name);
  try {
    const response = await fetch("/api/convert", {
      method: "POST",
      headers: {
        "X-Cone-Roblox-Api-Key": apiKeyInput.value.trim(),
        "X-Cone-Roblox-User-Id": userIDInput.value.trim(),
      },
      body,
    });
    await consumeEvents(response);
  } catch (error) {
    showError(error instanceof Error ? error.message : String(error));
  } finally {
    removeFile.disabled = false;
    apiKeyInput.disabled = false;
    userIDInput.disabled = false;
    updateConvertState();
  }
});
