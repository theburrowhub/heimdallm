Pod::Spec.new do |spec|
  spec.name = 'Sparkle'
  spec.version = '2.9.6'
  spec.summary = 'A software update framework for macOS'
  spec.homepage = 'https://sparkle-project.org'
  spec.documentation_url = 'https://sparkle-project.org/documentation/'
  spec.license = { :type => 'MIT', :file => 'LICENSE' }
  spec.authors = {
    'Zorg' => 'zorgiepoo@gmail.com',
    'Kornel Lesiński' => 'pornel@pornel.net',
    'Jake Petroules' => 'jake.petroules@petroules.com',
    'C.W. Betts' => 'computers57@hotmail.com',
  }
  spec.platform = :osx, '10.13'

  # CocoaPods' HTTP downloader verifies this digest before extracting the
  # release archive. Keep the URL, version, and digest in one reviewed file.
  spec.source = {
    :http => 'https://github.com/sparkle-project/Sparkle/releases/download/2.9.6/Sparkle-2.9.6.tar.xz',
    :sha256 => '52bf9e88cdd972fc0c81501377a880e90d47031bd8ca5462488f843e2609e192',
  }

  spec.source_files = 'Sparkle.framework/Versions/B/Headers/*.h'
  spec.public_header_files = 'Sparkle.framework/Versions/B/Headers/*.h'
  spec.preserve_paths = ['bin/*', 'Symbols']
  spec.vendored_frameworks = 'Sparkle.framework'
  spec.xcconfig = {
    'FRAMEWORK_SEARCH_PATHS' => '"${PODS_ROOT}/Sparkle"',
    'LD_RUNPATH_SEARCH_PATHS' => '@loader_path/../Frameworks',
  }
  spec.requires_arc = true
end
