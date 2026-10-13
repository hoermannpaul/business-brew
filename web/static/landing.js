(() => {
  "use strict";

  const copy = {
    dev: {
      benefitsTitle: "Fits your workflow.\nConnects your team.",
      benefitsIntro: "A small wrapper around your installers. A shared source of truth for everyone.",
      feature1Title: "Your terminal stays yours.",
      feature1Body: "Use brew, winget, apt, or dnf as usual. Company provisioning puts Business Brew in place, with no project file required for native commands.",
      feature1Detail: "FAMILIAR COMMANDS, CONNECTED",
      feature2Title: "Know where you stand.",
      feature2Body: "Approved packages use the real installer. Blocked packages create or reuse a request, with a tracking link so you can follow along.",
      feature2Detail: "CLEAR REQUESTS, LESS GUESSWORK",
      feature3Title: "The right version. Really.",
      feature3Body: "Package, version, source, and architecture are checked against the published catalog. The native installation is then verified.",
      feature3Detail: "EXACT TOOLS, EXPLICIT APPROVAL",
      workflowTitle: "One workflow.\nEveryone on the same page.",
      workflowIntro: "Approvals belong to your company. Installs stay in the developer’s hands.",
      step1Title: "Run your command.",
      step1Body: "Business Brew checks the resolved package against your company’s published catalog. Approved and available? The native installer takes over.",
      step2Title: "Get a clear next step.",
      step2Body: "Need approval or a package that isn’t available? A request is created or reused in your configured ticket system. Follow its tracking link.",
      step3Title: "Rerun. Get back to work.",
      step3Body: "Once IT publishes the grant and the package is available, rerun the same command. Business Brew checks again before installation.",
      setupTitle: "Good tools.\nIn good company.",
      setupIntro: "Business Brew is provisioned by your IT team. Once your company-managed profile and installer shims are in place, use your normal commands.",
      setupCTA: "Revisit the workflow"
    },
    management: {
      benefitsTitle: "A shared catalog.\nA clear set of permissions.",
      benefitsIntro: "Keep software decisions explicit, without replacing your developers’ everyday tools.",
      feature1Title: "Choose the approved tools.",
      feature1Body: "Define exact software versions, native packages, sources, platforms, and architectures. Save a draft and publish when it’s ready for developers.",
      feature1Detail: "ONE PUBLISHED SOURCE OF TRUTH",
      feature2Title: "Set the right boundaries.",
      feature2Body: "Grant access across your organization or restrict it to a project, a developer, or both. Issue and revoke credentials through the admin console.",
      feature2Detail: "EXPLICIT GRANTS, MANAGED ACCESS",
      feature3Title: "Meet requests where they are.",
      feature3Body: "Route approval and provisioning requests to your existing request system. A ticket decision stays separate from the catalog grant you publish.",
      feature3Detail: "YOUR SYSTEM, YOUR REVIEW PROCESS",
      workflowTitle: "You set the catalog.\nYour team keeps moving.",
      workflowIntro: "Review requests in your existing system. Publish permissions when you’re ready.",
      step1Title: "Define your catalog.",
      step1Body: "Add exact package targets and their approved sources. Choose organization, project, or developer grants. Draft changes stay private until publication.",
      step2Title: "Review what teams need.",
      step2Body: "Blocked installs create or reuse requests in your configured system. Review them there, and update the catalog’s software targets or grants as needed.",
      step3Title: "Publish with intention.",
      step3Body: "Explicitly publish the reviewed draft. Developers then rerun their command, which checks the current catalog and native availability before installing.",
      setupTitle: "Your infrastructure.\nYour software decisions.",
      setupIntro: "Run Business Brew on your company’s infrastructure. Manage the catalog and credentials in the existing admin console, then provision your developers.",
      setupCTA: "Open the admin console"
    }
  };

  const managementURL = document.body.dataset.managementUrl;
  copy.management.setupCTA = document.body.dataset.managementLabel;
  const output = document.getElementById("terminal-output");
  const stage = document.getElementById("terminal-stage");
  const pauseButton = document.getElementById("demo-pause");
  const motion = window.matchMedia("(prefers-reduced-motion: reduce)");
  const tabs = [...document.querySelectorAll("[data-perspective]")];
  let perspective = "dev";
  let paused = false;
  let finished = false;
  let index = 0;
  let character = 0;
  let typingLine = null;
  let timer = null;
  let pendingDelay = 0;

  // All output is illustrative; only shell commands receive the typing effect.
  const transcript = [
    { text: "# Your usual command. A little more connected.", className: "terminal-comment", delay: 500, stage: "A familiar command" },
    { text: "brew install node@22", command: true, delay: 550 },
    { text: "Business Brew: node 22.14.0 needs approval; request demo-42 [pending] https://tickets.example/42", className: "terminal-warning", delay: 800, stage: "Approval request created" },
    { text: "Business Brew: installation stopped; no packages in this command were installed. Rerun the same command after approval or provisioning.", className: "terminal-muted", delay: 2200 },
    { text: "↳ Later: IT reviews the request and publishes a catalog grant.", className: "terminal-event", delay: 1800, stage: "IT publishes approval · demo event" },
    { text: "brew install node@22", command: true, delay: 650, stage: "Developer reruns the same command" },
    { text: "==> Installing node@22", className: "terminal-muted", delay: 1100 },
    { text: "✓ Native installation complete; exact version verified.", className: "terminal-success", delay: 0, stage: "Approved, installed, and verified" }
  ];

  function addLine(item) {
    const line = document.createElement("div");
    line.className = "terminal-line " + (item.className || "");
    if (item.command) {
      const prompt = document.createElement("span");
      prompt.className = "prompt";
      prompt.textContent = "❯ ";
      line.append(prompt, document.createTextNode(item.text));
    } else {
      line.textContent = item.text;
    }
    output.append(line);
    return line;
  }

  function syncControls() {
    output.classList.toggle("paused", paused);
    pauseButton.disabled = motion.matches || finished;
    pauseButton.textContent = finished ? "Complete ✓" : motion.matches ? "Motion off" : paused ? "Play ▷" : "Pause Ⅱ";
    pauseButton.setAttribute("aria-label", paused ? "Resume terminal animation" : "Pause terminal animation");
  }

  function stopTimer() {
    clearTimeout(timer);
    timer = null;
  }

  function schedule(delay) {
    pendingDelay = delay;
    stopTimer();
    if (!paused && !finished && perspective === "dev" && !document.hidden) {
      timer = setTimeout(tick, delay);
    }
  }

  function tick() {
    if (index >= transcript.length) {
      finished = true;
      syncControls();
      return;
    }
    const item = transcript[index];
    if (item.stage) stage.textContent = item.stage;
    if (item.command) {
      if (!typingLine) {
        typingLine = addLine({ command: true, text: "" });
        typingLine.classList.add("typing");
        character = 0;
      }
      typingLine.lastChild.textContent = item.text.slice(0, ++character);
      if (character < item.text.length) {
        schedule(55);
        return;
      }
      typingLine.classList.remove("typing");
      typingLine = null;
    } else {
      addLine(item);
    }
    output.scrollTop = output.scrollHeight;
    index++;
    schedule(item.delay);
  }

  function restart() {
    stopTimer();
    output.replaceChildren();
    index = 0;
    character = 0;
    typingLine = null;
    paused = false;
    finished = false;
    if (motion.matches) {
      transcript.forEach(addLine);
      stage.textContent = "Full demo · reduced motion";
      finished = true;
      output.scrollTop = 0;
    } else {
      stage.textContent = "A familiar command";
      schedule(250);
    }
    syncControls();
  }

  function selectPerspective(next, focusTab = false) {
    perspective = next;
    tabs.forEach(tab => {
      const active = tab.dataset.perspective === next;
      tab.setAttribute("aria-selected", String(active));
      tab.tabIndex = active ? 0 : -1;
      document.getElementById(tab.getAttribute("aria-controls")).hidden = !active;
      if (active && focusTab) tab.focus();
    });
    document.querySelectorAll("[data-copy]").forEach(element => {
      const lines = copy[next][element.dataset.copy].split("\n");
      element.replaceChildren();
      lines.forEach((line, i) => {
        if (i) element.append(document.createElement("br"));
        element.append(document.createTextNode(line));
      });
    });
    document.querySelectorAll("[data-setup]").forEach(element => {
      element.hidden = element.dataset.setup !== next;
    });
    document.getElementById("setup-cta").href = next === "management" ? managementURL : "#how-it-works";
    document.querySelector(".hero").setAttribute("aria-labelledby", "hero-title-" + next);
    document.getElementById("perspective-status").textContent = next === "dev" ? "Developer perspective selected." : "Management and IT perspective selected.";
    if (next === "management") stopTimer();
    else schedule(pendingDelay);
  }

  tabs.forEach((tab, i) => {
    tab.addEventListener("click", () => selectPerspective(tab.dataset.perspective));
    tab.addEventListener("keydown", event => {
      let next;
      if (event.key === "ArrowRight") next = (i + 1) % tabs.length;
      else if (event.key === "ArrowLeft") next = (i - 1 + tabs.length) % tabs.length;
      else if (event.key === "Home") next = 0;
      else if (event.key === "End") next = tabs.length - 1;
      else return;
      event.preventDefault();
      selectPerspective(tabs[next].dataset.perspective, true);
    });
  });
  pauseButton.addEventListener("click", () => {
    paused = !paused;
    if (paused) stopTimer();
    else schedule(pendingDelay);
    syncControls();
  });
  document.getElementById("demo-replay").addEventListener("click", restart);
  motion.addEventListener("change", restart);
  document.addEventListener("visibilitychange", () => {
    if (document.hidden) stopTimer();
    else schedule(pendingDelay);
  });
  restart();
})();
