const form = document.querySelector("#convert-form");
const setupGuide = document.querySelector("#convert-form .setup-guide");
const setupSummary = setupGuide.querySelector("summary");
const coneArt = document.querySelector("#cone-art");
const coneImage = document.querySelector("#cone-image");
const input = document.querySelector("#pack-input");
const apiKeyInput = document.querySelector("#api-key-input");
const userIDInput = document.querySelector("#user-id-input");
const secretToggle = document.querySelector("#secret-toggle");
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
const resultModal = document.querySelector("#result-modal");
const resultBackdrop = document.querySelector("#result-backdrop");
const copyButton = document.querySelector("#copy-button");
const closeResultButton = document.querySelector("#close-result-button");

let currentFile = null;
let resultJSON = "";
let audioContext = null;
let playedCompleteSound = false;

const coneImages = {
  idle: "/cone.png",
  accepted: "/cone_accepted.png",
  error: "/cone_error.png",
};

for (const source of Object.values(coneImages)) {
  const image = new Image();
  image.src = source;
}

const storageKeys = {
  remember: "cone.rememberCredentials",
  apiKey: "cone.robloxApiKey",
  userID: "cone.robloxUserId",
};

function tone(frequency, start, duration, volume = 0.025, type = "sine") {
  const oscillator = audioContext.createOscillator();
  const gain = audioContext.createGain();
  oscillator.type = type;
  oscillator.frequency.setValueAtTime(frequency, start);
  gain.gain.setValueAtTime(0.0001, start);
  gain.gain.exponentialRampToValueAtTime(volume, start + 0.012);
  gain.gain.exponentialRampToValueAtTime(0.0001, start + duration);
  oscillator.connect(gain);
  gain.connect(audioContext.destination);
  oscillator.start(start);
  oscillator.stop(start + duration + 0.02);
}

function playSound(name) {
  try {
    audioContext ||= new AudioContext();
    if (audioContext.state === "suspended") audioContext.resume();
    const now = audioContext.currentTime + 0.01;
    const sounds = {
      tap: [[510, 0, 0.045]],
      select: [[440, 0, 0.06], [660, 0.055, 0.08]],
      start: [[300, 0, 0.07], [420, 0.07, 0.08]],
      complete: [[523, 0, 0.08], [659, 0.07, 0.08], [784, 0.14, 0.13]],
      error: [[210, 0, 0.1], [150, 0.09, 0.14]],
      copy: [[720, 0, 0.07]],
    };
    for (const [frequency, delay, duration] of sounds[name] || []) {
      tone(frequency, now + delay, duration, name === "error" ? 0.018 : 0.022, "triangle");
    }
  } catch {
    // Sound is optional; conversion must work when Web Audio is unavailable.
  }
}

function setConeState(state) {
  const source = coneImages[state] || coneImages.idle;
  if (coneArt.dataset.state === state && coneImage.getAttribute("src") === source) return;
  coneArt.dataset.state = state;
  coneImage.src = source;
  coneArt.classList.remove("is-changing");
  void coneArt.offsetWidth;
  coneArt.classList.add("is-changing");
}

function createClickSpark(event) {
  if (event.button !== 0 || window.matchMedia("(prefers-reduced-motion: reduce)").matches) return;
  const spark = document.createElement("span");
  spark.className = "click-spark";
  spark.style.left = `${event.clientX}px`;
  spark.style.top = `${event.clientY}px`;
  for (let index = 0; index < 8; index += 1) {
    const ray = document.createElement("span");
    ray.className = "click-spark-ray";
    ray.style.setProperty("--spark-angle", `${index * 45}deg`);
    spark.append(ray);
  }
  document.body.append(spark);
  window.setTimeout(() => spark.remove(), 480);
}

function readSavedCredentials() {
  try {
    if (localStorage.getItem(storageKeys.remember) === "false") {
      rememberCredentials.checked = false;
      if (window.matchMedia("(max-width: 480px)").matches) setupGuide.open = false;
      return;
    }
    rememberCredentials.checked = true;
    apiKeyInput.value = localStorage.getItem(storageKeys.apiKey) || "";
    userIDInput.value = localStorage.getItem(storageKeys.userID) || "";
    if ((apiKeyInput.value && userIDInput.value) || window.matchMedia("(max-width: 480px)").matches) {
      setupGuide.open = false;
    }
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
    resetOutput();
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
  playSound("select");
}

function updateConvertState() {
  convertButton.disabled = !currentFile || !apiKeyInput.value.trim() || !userIDInput.value.trim();
}

function resetOutput() {
  progressPanel.hidden = true;
  closeResultModal(false);
  statusError.hidden = true;
  activityLog.replaceChildren();
  resultJSON = "";
  playedCompleteSound = false;
  copyButton.textContent = "Copy JSON";
  setConeState("idle");
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
  closeResultModal(false);
  statusLabel.textContent = "Couldn’t convert pack";
  statusDetail.textContent = "Check the file and try again.";
  statusError.textContent = message;
  statusError.hidden = true;
  appendLog(message, true);
  setConeState("error");
  playSound("error");
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
    appendLog(progress.error ? `${message}: ${progress.error}` : message, Boolean(progress.error));
    return;
  }
  if (progress.stage === "complete") {
    updateProgress(100, "Ported", message);
    appendLog(message);
    if (!playedCompleteSound) {
      playedCompleteSound = true;
      playSound("complete");
    }
    return;
  }
  if (progress.stage === "notifying") {
    updateProgress(100, "Ported", message);
    appendLog(progress.error ? `${message}: ${progress.error}` : message, Boolean(progress.error));
  }
}

function handleResult(event) {
  resultJSON = JSON.stringify(event.result);
  resultModal.hidden = false;
  document.body.classList.add("modal-open");
  setConeState("accepted");
  window.requestAnimationFrame(() => copyButton.focus({ preventScroll: true }));
}

function closeResultModal(withSound = true) {
  if (resultModal.hidden) return;
  if (withSound) playSound("tap");
  resultModal.hidden = true;
  document.body.classList.remove("modal-open");
  convertButton.focus({ preventScroll: true });
}

async function copyJSON() {
  if (!resultJSON) return;
  try {
    await navigator.clipboard.writeText(resultJSON);
  } catch {
    const field = document.createElement("textarea");
    field.value = resultJSON;
    field.setAttribute("readonly", "");
    field.className = "copy-fallback";
    document.body.append(field);
    field.select();
    document.execCommand("copy");
    field.remove();
  }
  copyButton.textContent = "Copied";
  playSound("copy");
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
removeFile.addEventListener("click", () => {
  playSound("tap");
  setFile(null);
});
copyButton.addEventListener("click", copyJSON);
closeResultButton.addEventListener("click", () => closeResultModal());
resultBackdrop.addEventListener("click", () => closeResultModal());
document.addEventListener("keydown", (event) => {
  if (event.key === "Escape" && !resultModal.hidden) closeResultModal();
});
secretToggle.addEventListener("click", () => {
  playSound("tap");
  const showing = apiKeyInput.type === "text";
  apiKeyInput.type = showing ? "password" : "text";
  secretToggle.textContent = showing ? "Show" : "Hide";
  secretToggle.setAttribute("aria-label", showing ? "Show API key" : "Hide API key");
  secretToggle.setAttribute("aria-pressed", String(!showing));
  apiKeyInput.focus({ preventScroll: true });
});
setupSummary.addEventListener("click", () => playSound("tap"));
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
dropZone.addEventListener("pointermove", (event) => {
  const bounds = dropZone.getBoundingClientRect();
  dropZone.style.setProperty("--spot-x", `${event.clientX - bounds.left}px`);
  dropZone.style.setProperty("--spot-y", `${event.clientY - bounds.top}px`);
});
dropZone.addEventListener("pointerleave", () => {
  dropZone.style.removeProperty("--spot-x");
  dropZone.style.removeProperty("--spot-y");
});
document.addEventListener("pointerdown", createClickSpark);

form.addEventListener("submit", async (event) => {
  event.preventDefault();
  if (!currentFile) return;
  setupGuide.open = false;
  resetOutput();
  convertButton.disabled = true;
  removeFile.disabled = true;
  apiKeyInput.disabled = true;
  userIDInput.disabled = true;
  secretToggle.disabled = true;
  convertButton.textContent = "Porting...";
  playSound("start");
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
    secretToggle.disabled = false;
    convertButton.textContent = "Port pack";
    updateConvertState();
  }
});
