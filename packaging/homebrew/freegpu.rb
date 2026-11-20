cask "freegpu" do
  arch arm: "arm64", intel: "x86_64"

  version "1.0.0"
  sha256 :no_check

  url "https://github.com/Anigunda/freegpu/releases/download/v#{version}/FreeGPU-v#{version}-#{arch}.dmg"
  name "FreeGPU"
  desc "Native macOS Colab GPU Terminal & local OpenAI gateway for OpenCode"
  homepage "https://github.com/Anigunda/freegpu"

  app "FreeGPU.app"
  binary "#{appdir}/FreeGPU.app/Contents/MacOS/FreeGPU", target: "freegpu"

  zap trash: [
    "~/.freegpu",
  ]
end
