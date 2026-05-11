const readline = require("node:readline");

function ask(question, { input = process.stdin, output = process.stderr } = {}) {
  const rl = readline.createInterface({ input, output });
  return new Promise((resolve) => {
    rl.question(question, (answer) => {
      rl.close();
      resolve(answer.trim());
    });
  });
}

function askHidden(question, { input = process.stdin, output = process.stderr } = {}) {
  if (!input.isTTY || !output.isTTY || typeof input.setRawMode !== "function") {
    return ask(question, { input, output });
  }

  return new Promise((resolve, reject) => {
    let value = "";
    const wasRaw = input.isRaw;

    function cleanup() {
      input.off("data", onData);
      input.setRawMode(wasRaw);
      output.write("\n");
    }

    function onData(buffer) {
      const text = buffer.toString("utf8");
      for (const ch of text) {
        if (ch === "\u0003") {
          cleanup();
          reject(new Error("Cancelled"));
          return;
        }
        if (ch === "\r" || ch === "\n") {
          cleanup();
          resolve(value);
          return;
        }
        if (ch === "\u007f" || ch === "\b") {
          value = value.slice(0, -1);
          continue;
        }
        if (ch >= " ") {
          value += ch;
        }
      }
    }

    output.write(question);
    input.setRawMode(true);
    input.resume();
    input.on("data", onData);
  });
}

function parseCredentialsInput(text) {
  const lines = String(text || "").split(/\r?\n/);
  return {
    email: (lines[0] || "").trim(),
    password: lines[1] || "",
  };
}

function readAll(input) {
  return new Promise((resolve, reject) => {
    let text = "";
    input.setEncoding("utf8");
    input.on("data", (chunk) => {
      text += chunk;
    });
    input.on("error", reject);
    input.on("end", () => resolve(text));
  });
}

async function askCredentials({ input = process.stdin, output = process.stderr } = {}) {
  if (!input.isTTY) {
    const parsed = parseCredentialsInput(await readAll(input));
    if (!parsed.email || !parsed.password) {
      throw new Error("Non-interactive login expects email and password on stdin, one per line.");
    }
    return parsed;
  }
  return {
    email: await ask("Email: ", { input, output }),
    password: await askHidden("Password: ", { input, output }),
  };
}

async function choose(label, choices, { input = process.stdin, output = process.stderr } = {}) {
  if (!Array.isArray(choices) || choices.length === 0) {
    throw new Error(`No ${label} available.`);
  }
  if (choices.length === 1) {
    output.write(`Using ${label}: ${choices[0].label}\n`);
    return choices[0].value;
  }

  output.write(`${label}:\n`);
  choices.forEach((choice, index) => {
    output.write(`  ${index + 1}. ${choice.label}\n`);
  });

  while (true) {
    const answer = await ask(`Choose ${label} [1-${choices.length}]: `, { input, output });
    const n = Number.parseInt(answer, 10);
    if (Number.isInteger(n) && n >= 1 && n <= choices.length) {
      return choices[n - 1].value;
    }
    output.write(`Enter a number from 1 to ${choices.length}.\n`);
  }
}

module.exports = {
  ask,
  askCredentials,
  askHidden,
  choose,
  parseCredentialsInput,
};
